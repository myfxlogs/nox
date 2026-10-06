// pipeline_resume_helpers.go —— D-189 P0-4：ResumeClose 辅助函数（broker preflight / 下单 / 历史回填）。
// 从 pipeline_resume.go 拆出（>300 行软警告）。
package exec

import (
	"arb/internal/keydomain"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"time"

	"arb/internal/store/pgstore"
)

// ResumeClose D-189 P0-4：重触发 CLOSING 持仓的平仓流程（reconcile 超时自动重试入口）。
// 幂等：CLOSED/FAILED 直接返回 nil；只有 CLOSING 进入核心；其他状态返回 error。
// 不调 ClaimClose（position 已是 CLOSING）；走与 ClosePosition 相同的 leg 处理 + 残量校验 + PnL 汇总 + CLOSED。
// D-407 S1：per-position try-lock（锁挂本入口=三调用方统一收敛）；持锁中 → ErrCloseInFlight。
func (p *Pipeline) ResumeClose(ctx context.Context, posID int64) error {
	if p.store == nil {
		return fmt.Errorf("pipeline store not wired")
	}
	unlock, ok := p.tryLockClose(posID)
	if !ok {
		return fmt.Errorf("%w pos=%d", ErrCloseInFlight, posID)
	}
	defer unlock()

	pos, err := p.store.GetPosition(ctx, posID)
	if err != nil {
		return fmt.Errorf("get position %d: %w", posID, err)
	}
	if pos == nil {
		return fmt.Errorf("position %d not found", posID)
	}
	switch pos.Status {
	case "CLOSED", "FAILED":
		return nil // 幂等
	case "CLOSING":
		// 进入核心
	default:
		return fmt.Errorf("position %d status=%s not CLOSING (ResumeClose only for CLOSING)", posID, pos.Status)
	}
	return p.closePositionCore(ctx, posID, false)
}

// brokerPreflight D-189 P0-4 S4-2：查 verifier 确认腿是否仍在仓。
// 返回 stillOpen=true 表示 broker 仍有该腿（需发平仓单）；stillOpen=false 表示 broker 已无腿（需历史回填）。
// fill 非零表示已有成交回执（CEX CLOSE/FILLED 权威例外，D-120）；verifier 未接线或查询失败 → fail-closed。
func (p *Pipeline) brokerPreflight(ctx context.Context, posID int64, leg pgstore.PositionLeg) (bool, Fill, error) {
	if p.verifier == nil {
		return false, Fill{}, fmt.Errorf("verifier not wired")
	}
	vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	orders, err := p.verifier.QueryOpenedOrders(vctx, leg.Venue)
	if err != nil {
		// CEX venue 无 verifier querier（D-120：CEX 平仓 FILLED=权威）。
		if !isMTVenueKey(leg.Venue) {
			return true, Fill{}, nil // CEX 腿：假定仍在仓，走正常平仓路径
		}
		return false, Fill{}, fmt.Errorf("verify %s: %w", leg.Venue, err)
	}
	// MT 腿按 VenueKey+原 OrderID 匹配；CEX 腿按 VenueKey+Symbol+Side+PosSide 匹配（D-195-C2 §6.3 加 position_side）。
	for _, o := range orders {
		if isMTVenueKey(leg.Venue) {
			if o.Ticket == leg.OrderID {
				return true, Fill{}, nil
			}
		} else {
			if o.Symbol == leg.Symbol && o.Side == leg.Side {
				// D-195-C2 §6.3：CEX 聚合仓 position_side 维度匹配。
				// OKX long_short_mode 下 long/short 分列，PosSide 必须匹配才确认是同一持仓。
				// Gate/Binance 从 size 正负推导 PosSide（long/short），与 leg.Side 推导的 expectedPosSide 一致。
				// o.PosSide 为空（旧适配器或 MT 透传）→ 回退到 Symbol+Side 匹配（向后兼容）。
				if o.PosSide == "" {
					return true, Fill{}, nil
				}
				expectedPosSide := "long"
				if leg.Side == "SELL" {
					expectedPosSide = "short"
				}
				if o.PosSide == expectedPosSide {
					return true, Fill{}, nil
				}
			}
		}
	}
	return false, Fill{}, nil // broker 已无该腿 → 历史回填路径
}

// placeCloseOrder D-189 P0-4：发平仓单（MT CloseOrder / CEX PlaceOrder 反向）。
// D-195-C2 §6.4：qty 参数支持部分成交残量继续（全量时 = leg.EffectiveQty()，R6 成交口径）。
func (p *Pipeline) placeCloseOrder(ctx context.Context, posID int64, leg pgstore.PositionLeg, closeSide string, qty float64) (Fill, error) {
	exe, ok := p.registry.Get(leg.Venue)
	if !ok {
		return Fill{}, fmt.Errorf("no executor for venue %s", leg.Venue)
	}
	if isMTVenueKey(leg.Venue) {
		return exe.CloseOrder(ctx, OrderRef{OrderID: leg.OrderID, Venue: leg.Venue})
	}
	if pc, ok := exe.(Prechecker); ok {
		if pcErr := pc.Precheck(ctx, Order{
			Symbol: leg.Symbol, Side: closeSide, Qty: qty, Venue: leg.Venue,
		}); pcErr != nil {
			return Fill{}, fmt.Errorf("precheck: %w", pcErr)
		}
	}
	// D-473 S4：PositionSide 仅 perp 腿置位（驱动 reduceOnly）；:spot 腿无持仓概念
	// 必须留空（下游 okx/binance spot 分支虽不消费，置空防语义混淆与误路由）。
	var positionSide string
	if keydomain.IsType(leg.Venue, "perp") {
		switch leg.Side {
		case "BUY":
			positionSide = "long"
		case "SELL":
			positionSide = "short"
		}
	}
	return exe.PlaceOrder(ctx, Order{
		Symbol: leg.Symbol, Side: closeSide, Qty: qty, Venue: leg.Venue,
		PositionSide: positionSide, PosID: posID,
	})
}

// backfillCloseFromHistory D-189 P0-4 S4-4：broker 已无腿但 CLOSE leg 缺失 → 从权威历史回填。
// pnlCalibrator 命中且 ClosePrice > 0 → 回填 CLOSE leg + PnL + cashflows；无法取得 → fail-closed。
// D-577：gate:perp 腿开/平不同 order_id——ticket 语义不可平移，
// 走 contract+ClosingStartedAt 窗回填（WS 落库帧首选→gate REST 兜底），同一尾部复用。
// D-015 ②（R1）：双源皆无命中时查 cex_private_events 平仓帧兜底写 EXTERNAL
// （gate/okx/binance 三所通用，external_close_backfill.go）；MT venue 仍走原 fail-closed。
func (p *Pipeline) backfillCloseFromHistory(ctx context.Context, pos *pgstore.Position, leg pgstore.PositionLeg, fx float64) error {
	posID := pos.ID
	var hp *HistoryPnL
	var err error
	var missWhy string
	exe, _ := p.registry.Get(leg.Venue)
	_, isCexExec := exe.(*CEXExecutor)
	if keydomain.BareOf(leg.Venue) == "gate" && keydomain.IsType(leg.Venue, "perp") && isCexExec {
		since := time.Time{}
		if pos.ClosingStartedAt != nil {
			since = *pos.ClosingStartedAt
		} else if pos.OpenedAt != nil {
			since = *pos.OpenedAt
		}
		hp, err = p.cexCloseFill(ctx, leg, since)
		missWhy = "CEX 事件/REST 均无命中"
	} else {
		if p.pnlCalibrator != nil {
			hp, err = p.pnlCalibrator.QueryHistoryPnL(ctx, leg.Venue, leg.OrderID, 3)
			missWhy = fmt.Sprintf("历史查证无命中（票号 %s）", leg.OrderID)
		} else {
			missWhy = "无历史查证器"
		}
	}
	if err != nil || hp == nil || hp.ClosePrice <= 0 {
		// D-015 ②（R1）：历史查证无命中 → cex_private_events 平仓帧兜底（venue 侧
		// 自证已平）→ 命中写 EXTERNAL 腿+现金流收尾；仍无命中才告警。
		hit, ferr := p.externalCloseFallback(ctx, pos, leg, fx)
		if ferr != nil {
			return fmt.Errorf("external close evidence: %w", ferr)
		}
		if hit {
			return nil
		}
		msg := fmt.Sprintf("持仓 #%d 第 %d 腿（%s）券商侧已无此腿且%s——平仓盈亏无法核实，需人工核对", posID, leg.LegNo, leg.Venue, missWhy)
		if aerr := p.store.InsertPositionAlert(ctx, posID, "critical", msg); aerr != nil {
			log.Printf("[exec] pos %d alert write: %v", posID, aerr)
		}
		return fmt.Errorf("no authoritative history for backfill")
	}
	closeSide := "SELL"
	if leg.Side == "SELL" {
		closeSide = "BUY"
	}
	closePrice := hp.ClosePrice
	filledQty := leg.EffectiveQty() // D-346：回填无独立 fill 回执，成交口径=OPEN 腿实际持仓量
	// D-599 S1：历史回填腿同样落 fee——hp.Commission（计价币）×fx→USD（pos505 实证：
	// cashflow −2.08 有值而腿 fee_usd NULL，费到了现金流却没到腿行）。
	closeLeg := &pgstore.PositionLeg{
		PositionID: posID, LegNo: leg.LegNo, Phase: "CLOSE", AttemptNo: 1, Venue: leg.Venue, Account: leg.Account,
		Symbol: leg.Symbol, Side: closeSide, Qty: leg.EffectiveQty(), PriceExpected: leg.PriceExpected, // D-311 R6：回填成交量口径
		PriceActual: &closePrice, OrderID: leg.OrderID, Status: "FILLED",
		FilledQty: &filledQty, AvgFillPrice: &closePrice, // D-346：488/489/490 验尸=CLOSE 腿恒 NULL
		FeeUSD: positiveFeePtr(math.Abs(hp.Commission) * fx), // MT5 comm 负值口径→取绝对值（persistOpenLegs 同款）
	}
	if err := p.store.CreatePositionLeg(ctx, closeLeg); err != nil && !errors.Is(err, pgstore.ErrTerminalState) {
		return fmt.Errorf("backfill CLOSE leg: %w", err)
	}
	if err := p.writeCloseCashflows(ctx, posID, *closeLeg, hp.Swap, hp.Commission, 0, fx); err != nil {
		return fmt.Errorf("backfill cashflow: %w", err)
	}
	return nil
}

// backfillMissingCashflows D-189 P0-4 S4-5：已有 CLOSE leg 但 cashflows 缺失 → 从权威历史补回。
// D-013：commission 幂等键=leg:<close 腿 id>，须取该腿 CLOSE 行（OPEN 行 ID 不同键）。
func (p *Pipeline) backfillMissingCashflows(ctx context.Context, posID int64, leg pgstore.PositionLeg, fx float64) error {
	if p.pnlCalibrator == nil {
		return nil
	}
	hp, err := p.pnlCalibrator.QueryHistoryPnL(ctx, leg.Venue, leg.OrderID, 3)
	if err != nil || hp == nil {
		return nil
	}
	if hp.Swap == 0 && hp.Commission == 0 {
		return nil
	}
	closes, err := p.store.ListPositionLegsByPhase(ctx, posID, "CLOSE")
	if err != nil {
		return fmt.Errorf("backfill close legs pos=%d: %w", posID, err)
	}
	for i := range closes {
		if closes[i].LegNo == leg.LegNo {
			return p.writeCloseCashflows(ctx, posID, closes[i], hp.Swap, hp.Commission, 0, fx)
		}
	}
	return nil // 无 CLOSE 腿行→无从挂腿键，静默（主流程会走 closeBackfill 建新腿）
}

// ErrCloseInFlight D-407 S1：ResumeClose 单飞哨兵——同 posID 平仓执行中再次调用
// 快速失败（原阻塞等待=快试驱动/30min 看护/手动 RPC 三路并发会排队双发平仓单）。
// 调用方按可重入处理：log 不告警。
var ErrCloseInFlight = errors.New("close already in flight")

// closeMutex 取 posID 的 per-position mutex（closeMu 保护 closeSem map，惰性创建）。
// 调用前须保证 closeSem 非 nil。
func (p *Pipeline) closeMutex(posID int64) *sync.Mutex {
	p.closeMu.Lock()
	mu, ok := p.closeSem[posID]
	if !ok {
		mu = &sync.Mutex{}
		p.closeSem[posID] = mu
	}
	p.closeMu.Unlock()
	return mu
}

// lockClose D-189 P0-4：per-position close 锁（阻塞）。返回 unlock 函数。
// 零值 Pipeline（closing/closeSem nil）→ no-op（测试安全）。
func (p *Pipeline) lockClose(posID int64) func() {
	if p.closeSem == nil {
		return func() {} // 零值 Pipeline 安全
	}
	mu := p.closeMutex(posID)
	mu.Lock()
	return mu.Unlock
}

// tryLockClose D-407 S1：lockClose 的 try 变体——持锁中返回 (nil,false) 不阻塞。
// 零值 Pipeline（closeSem nil）→ (noop, true)（测试安全，同 lockClose 语义）。
func (p *Pipeline) tryLockClose(posID int64) (func(), bool) {
	if p.closeSem == nil {
		return func() {}, true
	}
	mu := p.closeMutex(posID)
	if !mu.TryLock() {
		return nil, false
	}
	return mu.Unlock, true
}
