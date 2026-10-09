// leg_actionable.go —— D-495 S2：腿可平性谓词（双腿异时市可平性闸的唯一实现，
// 决策层/执行层/入场预检三处共用，禁第二份——KB same-name-helper-opposite-fail-semantics）。
//
// 判据本体（KB heartbeat-quote-fools-freshness-gate，8-30 #464 开仓侧同型前科）：
// 可交易 = 窗口内该 symbol 的 bid 值发生过变化（DISTINCT bid ≥ 2）。
// 窗口内单值（含重启冷启动首见）→ 不可判可交易；tick 时间戳新鲜/有帧 ≠ 可交易。
//
// 键域映射（D-495 实测钉死，KB cross-domain-key-reuse-silent-miss）：
// ticks.venue = 裸账号/交易所名（"3001157"/"okx"），腿侧 VenueKey 是键形
// （"3001157:mt5"/"okx:perp"）→ keydomain.BareOf 取裸键（D-496/D-497 键域单源收编）。
// MT 腿 symbol=venue 原生名直查（ticks.symbol 存原生名非 canonical）；CEX 腿走
// bus.CexSymbolKey 归一键（XAU-USDT-SWAP→XAUUSDT）。禁 canonical、禁兄弟品种代查
// （symbol 归属律，D-466 同律推广）。
package exec

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
)

// ErrLegNotActionable D-495 S4：腿不可平性哨兵（ClaimClose 前置闸拒平信号，
// 调用方 errors.Is 识别后透传可读文案/前端 confirm 逃逸门）。
var ErrLegNotActionable = errors.New("leg not actionable")

// LegNotActionableError 带结构的拒平原因（venue/symbol/reason 透出供文案）。
type LegNotActionableError struct {
	VenueKey string
	Symbol   string
	Reason   string
}

func (e *LegNotActionableError) Error() string {
	return fmt.Sprintf("%s: %s@%s %s", ErrLegNotActionable, e.Symbol, e.VenueKey, e.Reason)
}

func (e *LegNotActionableError) Is(target error) bool { return target == ErrLegNotActionable }

// legActionableStore LegActionable 依赖面（*pgstore.Store 天然实现；测试可 mock）。
type legActionableStore interface {
	TicksPriceChanged(ctx context.Context, venue, symbol string, window time.Duration) (changed bool, n int, err error)
	CanonicalByBrokerSymbol(ctx context.Context, venue, brokerSymbol string) (string, bool, error)
}

// LegActionable 判定 venueKey×nativeSymbol 此刻是否有可交易行情。
// 返回 (true, "") 可行动；(false, reason) 不可行动（reason 供告警/错误文案）。
// 缺数据/查询失败一律 fail-closed 返回不可行动（exit-condition-missing-facts-fail-closed 同律）。
// trade_mode 由判定时刻即时探针取值（D-004 P1#2），不读 facts 缓存——
// 见 leg_trademode.go；CEX 腿无此概念跳过。
func LegActionable(ctx context.Context, st legActionableStore, prober TradeModeProber, venueKey, nativeSymbol, side string, closing bool) (bool, string) {
	if st == nil {
		return false, "store not wired"
	}
	tickVenue := keydomain.BareOf(venueKey)
	tickSymbol := nativeSymbol
	if bus.IsCexVenueKey(venueKey) {
		tickSymbol = bus.CexSymbolKey(nativeSymbol)
	} else {
		if canon, ok, err := st.CanonicalByBrokerSymbol(ctx, tickVenue, nativeSymbol); err == nil && ok {
			tickSymbol = canon // D-536：ticks 键域=canonical（写侧 tick_sampler 用 q.Symbol=canonical）
		}
		// err!=nil 或 ok=false：保留 nativeSymbol 走原路径——fail-closed 结局不变
		//（映射查不到→ticks 同键也查不到→照旧判死，行为不放大）。
		if ok, why := checkTradeMode(ctx, prober, tickVenue, tickSymbol, nativeSymbol, side, closing); !ok {
			return false, why
		}
	}
	if tickVenue == "" || tickSymbol == "" {
		return false, fmt.Sprintf("empty tick key venue=%q symbol=%q", tickVenue, tickSymbol)
	}

	window := legTickWindow()
	changed, n, err := st.TicksPriceChanged(ctx, tickVenue, tickSymbol, window)
	if err != nil {
		return false, fmt.Sprintf("%s@%s tick query: %v", tickSymbol, tickVenue, err)
	}
	if !changed {
		return false, fmt.Sprintf("%s@%s 窗口 %s 内 bid 无变化(n=%d)：休市/断流/冻结报价",
			tickSymbol, tickVenue, window, n)
	}
	return true, ""
}

// checkLegsActionable D-495 S4：position 全部 OPEN+FILLED 腿的 ClosePosition 前置闸。
// store 查询失败=普通错误（瞬时不进逃逸门语义）；腿不可行动=LegNotActionableError。
func (p *Pipeline) checkLegsActionable(ctx context.Context, posID int64) error {
	legs, err := p.store.ListPositionLegs(ctx, posID)
	if err != nil {
		return fmt.Errorf("leg actionable list legs %d: %w", posID, err)
	}
	for _, leg := range legs {
		if leg.Phase != "OPEN" || leg.Status != "FILLED" {
			continue
		}
		if ok, why := LegActionable(ctx, p.store, p.tradeModeProber(leg.Venue), leg.Venue, leg.Symbol, leg.Side, true); !ok {
			return &LegNotActionableError{VenueKey: leg.Venue, Symbol: leg.Symbol, Reason: why}
		}
	}
	return nil
}

// mergeNoteToken D-495 S4：position.note 合并写助手——SetPositionNote 是整字段覆盖
// （positions.go:259），裸写会吞 settle_failed:/pnl_estimated:/PROBE 等异类 token。
// 按 "; " 切分丢旧同 key 追加新 "key:value"；CLOSED 后 token 残留=历史痕迹不清理。
func mergeNoteToken(existing, key, value string) string {
	var kept []string
	for _, tok := range strings.Split(existing, "; ") {
		tok = strings.TrimSpace(tok)
		if tok == "" || strings.HasPrefix(tok, key+":") {
			continue // 丢旧同 key（幂等重写）
		}
		kept = append(kept, tok)
	}
	kept = append(kept, key+":"+value)
	return strings.Join(kept, "; ")
}

// legsActionableGate D-539：平仓执行核心全腿前置闸——任一 OPEN+FILLED 腿不可行动 →
// LegNotActionableError 哨兵（本轮整仓零发单，保持对冲持有；pos502 形态根治：旧语义
// 腿循环内逐腿跳过会先平亮腿、暗腿裸露一周末）+ await_session note 透出观测面。
// 判据复用 LegActionable 单源（与 checkLegsActionable 差别：本闸收集全部暗腿写 note
// 观测，后者首败即返=触发层拒平）；供 reconciler 下轮重试。调用方按 !force 调用。
func (p *Pipeline) legsActionableGate(ctx context.Context, posID int64, legs []pgstore.PositionLeg, note *string) error {
	var blocked []pgstore.PositionLeg
	for _, leg := range legs {
		if leg.Phase != "OPEN" || leg.Status != "FILLED" {
			continue
		}
		if ok, why := LegActionable(ctx, p.store, p.tradeModeProber(leg.Venue), leg.Venue, leg.Symbol, leg.Side, true); !ok {
			blocked = append(blocked, leg)
			log.Printf("[exec] pos %d leg-actionable gate block %s@%s: %s", posID, leg.Symbol, leg.Venue, why)
		}
	}
	if len(blocked) == 0 {
		return nil
	}
	p.writeAwaitSessionNote(ctx, posID, note, blocked)
	b := blocked[0]
	return &LegNotActionableError{VenueKey: b.Venue, Symbol: b.Symbol, Reason: "gate: 全腿前置闸，本轮整仓不发"}
}

// legActionableUnlessForce force 逃逸门短路（force=true 不查 ticks 直接可行动）。
func legActionableUnlessForce(ctx context.Context, st legActionableStore, prober TradeModeProber, leg pgstore.PositionLeg, force bool) (bool, string) {
	if force {
		return true, ""
	}
	return LegActionable(ctx, st, prober, leg.Venue, leg.Symbol, leg.Side, true)
}

// tradeModeProber 按 venueKey 从执行器注册表取 TradeModeProber（D-004 P1#2）：
// executor 缺席或未实现该接口 → nil，由 checkTradeMode 按开/平仓分支裁决。
func (p *Pipeline) tradeModeProber(venueKey string) TradeModeProber {
	if p.registry == nil {
		return nil
	}
	if exe, ok := p.registry.Get(venueKey); ok {
		if prober, ok := exe.(TradeModeProber); ok {
			return prober
		}
	}
	return nil
}

// allResidualAwaited 残留条目是否全部对应本轮 await_session 跳过腿
// （残留串形="venueKey ticket=orderID"，与 pipeline_legs_verify.go 产出对齐）。
func allResidualAwaited(residual []string, awaited []pgstore.PositionLeg) bool {
	set := make(map[string]bool, len(awaited))
	for _, l := range awaited {
		set[fmt.Sprintf("%s ticket=%s", l.Venue, l.OrderID)] = true
	}
	for _, r := range residual {
		if !set[r] {
			return false
		}
	}
	return true
}

// writeAwaitSessionNote await_session token 合并写（值=逗号连 venue:symbol 列表）。
// 返回是否产生新转场（note 变化=首报面）。写失败记日志不阻断平仓主路径。
func (p *Pipeline) writeAwaitSessionNote(ctx context.Context, posID int64, note *string, awaited []pgstore.PositionLeg) (newAwait bool) {
	if len(awaited) == 0 {
		return false
	}
	var toks []string
	for _, l := range awaited {
		toks = append(toks, l.Venue+":"+l.Symbol)
	}
	newNote := mergeNoteToken(*note, "await_session", strings.Join(toks, ","))
	if newNote == *note {
		return false
	}
	if err := p.store.SetPositionNote(ctx, posID, newNote); err != nil {
		log.Printf("[exec] pos %d await_session note write: %v", posID, err)
		return false
	}
	*note = newNote
	return true
}

// emitResidualAlert D-495 §1.3：broker 残留告警分级——残留全为已知 await_session
// 腿 → warn 且只在首次转场发（每周期 critical 刷屏禁令）；混入未知残留维持 critical。
func (p *Pipeline) emitResidualAlert(ctx context.Context, posID int64, residual []string, awaited []pgstore.PositionLeg, newAwait bool) {
	msg := fmt.Sprintf("持仓 #%d 平仓后仍有残留腿：%s", posID, strings.Join(residual, ", "))
	log.Printf("[exec] %s", msg)
	level, emit := "critical", true
	if len(awaited) > 0 && allResidualAwaited(residual, awaited) {
		level, emit = "warn", newAwait
	}
	if !emit {
		return
	}
	if err := p.store.InsertPositionAlert(ctx, posID, level, msg); err != nil {
		log.Printf("[exec] pos %d alert write: %v", posID, err)
	}
}

// verifyResidualOrErr 平仓后残量校验段（自 pipeline_resume.go 提取压行）：
// verifier 未接线/查询失败=fail-closed error；残留>0 → emitResidualAlert 分级后报错。
func (p *Pipeline) verifyResidualOrErr(ctx context.Context, posID int64, legs, awaited []pgstore.PositionLeg, newAwait bool) error {
	if p.verifier == nil {
		msg := fmt.Sprintf("持仓 #%d 残量校验器未接线，无法核对平仓结果", posID)
		log.Printf("[exec] %s", msg)
		if err := p.store.InsertPositionAlert(ctx, posID, "critical", msg); err != nil {
			log.Printf("[exec] pos %d alert write: %v", posID, err)
		}
		return fmt.Errorf("verifier not wired: position %d", posID)
	}
	residual, verifyErr := p.checkResidualLegs(ctx, legs)
	if verifyErr != nil {
		msg := fmt.Sprintf("持仓 #%d 平仓残量核对失败：%v", posID, verifyErr)
		log.Printf("[exec] %s", msg)
		if err := p.store.InsertPositionAlert(ctx, posID, "critical", msg); err != nil {
			log.Printf("[exec] pos %d alert write: %v", posID, err)
		}
		return fmt.Errorf("verify residual: %w", verifyErr)
	}
	if len(residual) > 0 {
		p.emitResidualAlert(ctx, posID, residual, awaited, newAwait)
		return fmt.Errorf("residual legs after close: %s", strings.Join(residual, ", "))
	}
	return nil
}

// legWindow 可平性判定窗口（默认 30min；D-145 env 收口——装配层 SetLegTickWindow
// 注入 cfg 值，本包不再直读 env）。
var legWindow = 30 * time.Minute

// SetLegTickWindow boot 注入判定窗（<=0 忽略；先于 pipeline 构造调用）。
func SetLegTickWindow(d time.Duration) {
	if d > 0 {
		legWindow = d
	}
}

// legTickWindow 采样器 60s 节流按事件写入——冻结报价产生同 bid 行，DISTINCT 判据天然识别。
func legTickWindow() time.Duration { return legWindow }
