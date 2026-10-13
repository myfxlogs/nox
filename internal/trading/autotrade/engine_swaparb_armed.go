// engine_swaparb_armed.go —— D-004 P1#5：swapArb「facts 武装 + quote 触发」分层。
// 全扫（facts 写后/订单事件/boot）只负责发现+冷却/止损过滤，产出 armed 候选集；
// quote 帧驱动纯内存 swapQuotePrecheck 镜像 quote 依赖门禁，合格才升权威
// trySwapArb（每组合每次武装至多一次）；5min 盲扫 ticker 删除，退化为
// facts 饥荒看门狗（只告警不扫）。
package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/data/bus"
	"nox/internal/platform/combo"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// swapFactsWatchdog swapArb facts 饥荒看门狗周期：mt_swap 1h 采集 ×2 容忍。
// 包级 var 供测试覆写（positionFallbackInterval 先例）。
var swapFactsWatchdog = 2 * time.Hour

// mrWatchdogInterval meanRevert 看门狗周期（原 60s 盲扫 ticker 的退化形态）。
var mrWatchdogInterval = 60 * time.Second

// armedCombo 全扫后武装的候选：quote 帧触发纯内存 precheck，合格升权威 trySwapArb。
// tried=本次武装内已升权威（无论成败——状态门禁失败只会因 facts/订单事件改变，
// 那些事件会全扫重建 armed；零轮询）。
type armedCombo struct {
	c     swapComboCandidate
	tried bool
}

// swapQuoteGates trySwapArb quote 依赖门禁的纯函数单源（D-004 P1#5 抽出）：
// 陈旧度 → 腿 spread 宽度 → 入场基差（当前帧重算，同 discover 公式
// (qShort.Bid−qLong.Ask)/qLong.Ask×1e4）→ 入场价差下限 → 上限。
// trySwapArb 权威路径与 swapQuotePrecheck 快路共用，禁两份判定。
// 返回 spreadBps 供调用方继续使用/观测。
func swapQuoteGates(qLong, qShort bus.Quote, now time.Time, cfg gate.Config) (spreadBps float64, reject bool, reason string) {
	// 修复⑪B N6：陈旧度复用 DetectorFreshnessTTL（env 化）
	if cfg.FreshnessTTL > 0 {
		if now.Sub(qLong.Time) > cfg.FreshnessTTL || now.Sub(qShort.Time) > cfg.FreshnessTTL {
			return 0, true, "stale quote"
		}
	}
	// 两腿 bid-ask spread 宽度门禁——任一腿 spread 过宽=流动性异常/报价不一致 → skip。
	if tooWide, r := legSpreadTooWide(qLong, qShort, cfg.SwapArbMaxLegSpreadBps); tooWide {
		return 0, true, r
	}
	// 入场基差合理性门禁——当前帧重算（qL.Ask<=0=盲腿 fail-closed，
	// 与 discover 侧 basisAvailable 同义）；max<=0 不校验（向后兼容）。
	basisBps, basisAvailable := 0.0, false
	if qLong.Ask > 0 {
		basisBps = (qShort.Bid - qLong.Ask) / qLong.Ask * 10000
		basisAvailable = true
	}
	if rej, r := entryBasisGateCheck(basisBps, basisAvailable, cfg.EntryBasisMaxBps); rej {
		return 0, true, "entry basis gate (" + r + ")"
	}
	if qLong.Ask <= 0 {
		return 0, true, "blind leg ask<=0" // spreadBps 无意义（除零 fail-closed）
	}
	spreadBps = (qShort.Bid - qLong.Ask) / qLong.Ask * 10000
	// 修复⑪B N6：入场成本容忍 env 化（NOX_SWAPARB_ENTRY_SPREAD_MIN_BPS，默认 -3）
	if spreadBps < cfg.EntrySpreadMinBps {
		return spreadBps, true, "spreadBps below min"
	}
	// 入场价差上限——spreadBps > max 拒绝（"太好的价差"=虚假价差/跨合约不同底层）。
	if rej, r := entrySpreadMaxGateCheck(spreadBps, cfg.EntrySpreadMaxBps); rej {
		return spreadBps, true, "entry spread max gate (" + r + ")"
	}
	return spreadBps, false, ""
}

// swapQuotePrecheck quote 帧纯内存 precheck（零 DB/RPC）：镜像 trySwapArb 的
// quote 依赖门禁（swapQuoteGates 单源）。false=本帧不升权威（下一帧再看）。
func (e *Engine) swapQuotePrecheck(c *swapComboCandidate) bool {
	if e.quotes == nil {
		return false
	}
	qLong, okL := e.quotes.Latest(c.canonical, c.vLong)
	qShort, okS := e.quotes.Latest(c.canonical, c.vShort)
	if !okL || !okS {
		return false // 盲腿：无报价不评估（trySwapArb 同判据）
	}
	_, reject, _ := swapQuoteGates(qLong, qShort, time.Now(), e.cfg)
	return !reject
}

// publishSwapArmed 武装 canonical 集快照 → atomic.Pointer 无锁发布给
// quoteKickDispatch（读侧零锁；每次全扫重建 armed 后调用）。
func (e *Engine) publishSwapArmed(armed map[string]*armedCombo) {
	set := make(map[string]struct{}, len(armed))
	for _, ac := range armed {
		set[ac.c.canonical] = struct{}{}
	}
	e.swapArmedCanon.Store(&set)
}

// swapArmedCanonical dispatch 侧查询：canonical 是否在当前武装集（nil-safe）。
func (e *Engine) swapArmedCanonical(symbol string) bool {
	set := e.swapArmedCanon.Load()
	if set == nil {
		return false
	}
	_, ok := (*set)[symbol]
	return ok
}

// swapArbWatchdogAlert facts 饥荒看门狗告警（只告警不扫；WATCHDOG 字样钉测试锚）。
func (e *Engine) swapArbWatchdogAlert(lastFactsKick time.Time) {
	msg := "swapArb facts starvation: factsKick 超时未到（mt_swap/WS funding 链断）"
	log.Printf("[autotrade] WATCHDOG %s (lastFactsKick=%v ago)", msg, time.Since(lastFactsKick).Round(time.Second))
	if e.AlertSink != nil {
		e.AlertSink("critical", "swapArb facts 饥荒看门狗："+msg)
	}
}

// trySwapArb 尝试开仓单个 swap 套利组合（D-072：名义额=broker 余额驱动，经 gate）。
// S2→D-004 P1#5：入场基差改由 swapQuoteGates 用当前帧报价重算
// （同 discover 公式）——quote 依赖值不跨事件沿传，armed 候选 quote 帧重判。
// swapSrc 由 discoverSwapCombos 传入（settled|spec|none），供 S4 门禁折扣判定 + 决策日志。
func (e *Engine) trySwapArb(ctx context.Context, strategy, canonical, venueLong, venueShort string, netSwapBps float64, swapSrc string) {
	// 修订：判死策略停提交（扫描+评估常开喂 rollup，禁用只拦提交无 REJECTED 行）
	// err → log + return（fail-closed，与 gate.go:75 一致）
	disabled, err := e.store.IsStrategyDisabled(ctx, strategy)
	if err != nil {
		log.Printf("[autotrade] IsStrategyDisabled error (fail-closed skip): %v", err)
		return
	}
	if disabled {
		return
	}
	qLong, okL := e.quotes.Latest(canonical, venueLong)
	qShort, okS := e.quotes.Latest(canonical, venueShort)
	if !okL || !okS {
		log.Printf("[autotrade] %s skip %s: blind leg rejected (quote missing, long=%v short=%v)", strategy, canonical, okL, okS)
		return
	}
	// P1#5：quote 依赖门禁整组走 swapQuoteGates 纯函数单源
	//（陈旧度/腿 spread/入场基差当前帧重算/价差上下限）——quote 快路同函数镜像。
	spreadBps, reject, reason := swapQuoteGates(qLong, qShort, time.Now(), e.cfg)
	if reject {
		log.Printf("[autotrade] %s skip %s: %s", strategy, canonical, reason)
		return
	}
	// 决策日志需要 basisBps——用同一公式当前帧值（gate 内部同源，不重算第二次）。
	basisBps := 0.0
	if qLong.Ask > 0 {
		basisBps = (qShort.Bid - qLong.Ask) / qLong.Ask * 10000
	}
	// swap facts 零值校验——任一腿 swap facts=0 但 broker 该 venue 有非零 swap 历史→fail-closed 拒绝。
	// 规格声明与实际结算不一致（如 PXBT BTCUSDT facts=0 但实际 -2.21/日）→ 不允许开仓。
	// 查询失败 → fail-closed 拒绝。
	if e.cfg.SwapFactsZeroCheckEnabled {
		if reason := e.swapFactsZeroCheck(ctx, canonical, venueLong, venueShort); reason != "" {
			log.Printf("[autotrade] %s skip %s: %s", strategy, canonical, reason)
			return
		}
	}
	// 名义额 = broker 余额驱动（fail-closed 三条件）
	notional, ok, reason := e.service.SwapArbNotional(canonical, venueLong, venueShort)
	if !ok {
		// 资金注入提醒：sizing 不足 → 发布 FundingNeedNotice 到 topic → 前端弹窗（不入 alerts 表）。
		// 每日每组合至多 1 条（内存节流）；非 sizing 原因（venue 离线/币种不符）仅 log 不弹窗。
		// 口径从 "balance" 改 "sizing"（minEq×ratio < minNotional，非旧全款门）。
		if strings.Contains(reason, "sizing") {
			e.maybePublishFundingNeed(canonical, venueLong, venueShort, reason)
		}
		log.Printf("[autotrade] %s skip %s %s/%s: %s", strategy, canonical, venueLong, venueShort, reason)
		return
	}
	// 决策日志带 swap_src（settled|spec）——S4 门禁折扣判定的依据。
	log.Printf("[autotrade] %s eval %s %s/%s: swap_src=%s netSwap=%.4f bps spreadBps=%.2f basisBps=%.2f",
		strategy, canonical, venueLong, venueShort, swapSrc, netSwapBps, spreadBps, basisBps)
	// 入场门禁三条——厚度+回收期往返+集中度。
	// probe 仓（swapSrc != settled）旁路厚度+回收期（无实测数据不卡厚度），
	// 集中度不旁路（同 venue×symbol 活跃腿 ≥2 仍拒，立项授权稿 §3）。
	willBeProbe := e.cfg.ProbeEnabled && swapSrc != swapSrcSettled
	// 厚度：净息差 ≥ MinNetCarryBps（spec 源 × SpecDiscount 折扣，settled 不折扣）。
	if !willBeProbe {
		if g := carryThicknessGate(netSwapBps, swapSrc, e.cfg.MinNetCarryBps, e.cfg.SpecDiscount); g.reject {
			log.Printf("[autotrade] %s skip %s: %s", strategy, canonical, g.reason)
			return
		}
		// 回收期往返：2×spreadBps / 净息差 ≤ MaxPaybackDays（2×=开+平对称假设）。
		if g := paybackRoundTripGate(spreadBps, netSwapBps, e.cfg.MaxPaybackDays); g.reject {
			log.Printf("[autotrade] %s skip %s: %s", strategy, canonical, g.reason)
			return
		}
	}
	// 集中度：同 venue×symbol 活跃腿 < MaxLegsPerSymbol（471/472 双空单场景拒第三腿）。
	if g := e.concentrationGate(ctx, canonical, venueLong, venueShort, e.cfg.MaxLegsPerSymbol); g.reject {
		log.Printf("[autotrade] %s skip %s: %s", strategy, canonical, g.reason)
		return
	}
	// venue 级敞口门——两腿 venue SWAPARB 合计裸露 + 新仓 ≤ equity × cap，否则 skip。
	if !e.service.SwapArbVenueExposureOK(ctx, venueLong, venueShort, notional) {
		log.Printf("[autotrade] %s skip %s: venue exposure cap", strategy, canonical)
		return
	}
	// A 修复：qty 定尺寸用 usdPerLot（买腿 ask 价）——原 notional/ask 是 USD÷JPY 币种混算。
	// qty = notional(USD) ÷ usdPerLot(买腿) = USD ÷ (CS×price÷USDJPY) → 手数按计价币正确换算。
	usdPerLotLong, ok := e.service.USDPerLotForLeg(canonical, venueLong, "ask")
	if !ok || usdPerLotLong <= 0 {
		log.Printf("[autotrade] %s skip %s: usdPerLot buy leg unavailable (fx/quote missing)", strategy, canonical)
		return
	}
	qty := notional / usdPerLotLong
	// H4.2：qty 低于买腿最小步 → debug 跳过（品种适配事实，不产 intent 不落 REJECTED）
	if e.service.QtyBelowMinStep(venueLong, qLong.VenueSymbol, qty) {
		log.Printf("[autotrade] %s skip %s: qty %.6f below min step (notional $%.0f too small for %s)",
			strategy, canonical, qty, notional, qLong.VenueSymbol)
		return
	}
	dedupKey := combo.Key(strategy, canonical, venueLong, venueShort)
	// 组合保证金预算观察版告警（只告警不拦截）+ 杠杆不对称提示。best-effort 不影响开仓。
	e.observeMarginBudget(canonical, venueLong, venueShort, notional)
	// [exec-entry:gate] D-262 M4.3①：正规径——intent.Execute→ExecuteMeanRevertWithCap。
	intent := gate.TradeIntent{
		Strategy:    strategy,
		Canonical:   canonical,
		VenueBuy:    venueLong,
		VenueSell:   venueShort,
		Qty:         qty,
		NotionalUSD: notional,
		DedupKey:    dedupKey,
		NetBps:      spreadBps,
	}
	// 超限折分：Execute 闭包捕获 intent 指针，读 intent.Qty/NotionalUSD
	// （gate 层折分时修改 intent 字段，闭包内读到折分后的值）
	intentPtr := &intent
	intent.Execute = func(ctx context.Context) (string, string, int64) {
		reqID := fmt.Sprintf("swaparb-%d", time.Now().UnixNano())
		status, msg, posID := e.service.ExecuteMeanRevertWithCap(ctx, canonical, venueLong, venueShort, reqID, intentPtr.Qty, spreadBps, intentPtr.NotionalUSD, strategy)
		if status != "FAILED" && posID > 0 {
			// 新持仓直接写组合键（无时间戳），与 opportunities.id 贯通
			gate.AuditWarn(ctx, e.store, fmt.Sprintf("持仓 #%d 机会ID 回写", posID),
				e.store.UpdatePositionOppID(ctx, posID, dedupKey))
			// 落盘 notional_usd=双腿口径（intentPtr.NotionalUSD 是单腿口径，×2=双腿合计）。
			// gate 折分后 intentPtr.NotionalUSD 已更新为 AdjustedNotional（gate.go:140），此处读折分后值。
			gate.AuditWarn(ctx, e.store, fmt.Sprintf("持仓 #%d 名义额回写", posID),
				e.store.UpdatePositionNotional(ctx, posID, intentPtr.NotionalUSD*2))
			// SWAPARB typed 双字段——ExecuteMeanRevertWithCap 已写 EntrySpreadBps，
			// 此处补 SwapBpsDaily + AnnualPct 覆盖写全
			swap := netSwapBps
			annual := netSwapBps * 365 / 100
			meta := pgstore.PositionMeta{
				EntrySpreadBps: &spreadBps,
				SwapBpsDaily:   &swap,
				AnnualPct:      &annual,
			}
			// CARRY expected_net_bps 落值——ExpectedUsd CARRY 分支读 netBps（年化百分点），
			// 不写则 expected_net_bps=0 → ExpectedUsd=0 → 结算恒 skip → verification 样本饿死。
			// SWAPARB 不写 NetBps（ExpectedUsd SWAPARB 分支读 swapBpsDaily，不读 netBps）。
			if strategy == "CARRY" {
				netBps := annual
				meta.NetBps = &netBps
			}
			gate.AuditWarn(ctx, e.store, fmt.Sprintf("持仓 #%d 元数据回写", posID),
				e.store.UpdatePositionMeta(ctx, posID, strategy, meta))
			// 双腿成交后写 expected_entry_spread_bps（成交价口径基准，止损用）。
			// = (buyActual − sellActual) / buyActual × 10000（BUY 成交=ask、SELL 成交=bid）。
			// 口径：entry=成交价(含滑点) / current=报价口径(不含滑点) → PnL 保守(止损偏早=安全)。
			// 该写失败=止损路径静默失能（P0 性质），warn 告警可见。
			if entryBps, ok := computeEntrySpreadFromLegs(ctx, e.store, posID); ok {
				gate.AuditWarn(ctx, e.store, fmt.Sprintf("持仓 #%d 入场价差基准回写（止损基准失写=P0）", posID),
					e.store.UpdateExpectedEntrySpreadBps(ctx, posID, entryBps))
			}
			// probe 仓标记——任一腿非 settled → note=PROBE。
			if willBeProbe {
				gate.AuditWarn(ctx, e.store, fmt.Sprintf("持仓 #%d 探针标记回写", posID),
					e.store.SetPositionNote(ctx, posID, "PROBE"))
				log.Printf("[autotrade] probe pos=%d marked: swap_src=%s (non-settled legs)", posID, swapSrc)
			}
		}
		return status, msg, posID
	}
	e.gate.Submit(ctx, &intent)
}
