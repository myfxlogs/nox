package exec

import (
	"arb/internal/keydomain"
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
)

// isContextTimeoutErr D-159 W1a R2：判断 err 是否为 context 超时/取消（MT OrderSend 超时→UNKNOWN 用）。
// 包含 context.DeadlineExceeded、context.Canceled 及 gRPC 层包装的 deadline exceeded。
func isContextTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	// gRPC DEADLINE_EXCEEDED 常以字符串包装（rpc error: code = DeadlineExceeded）。
	s := err.Error()
	return strings.Contains(s, "DeadlineExceeded") || strings.Contains(s, "context deadline exceeded")
}

// orderQtyFor LegSpec.Qty → exec.Order.Qty 执行器边界换算（D-311 R4）。
// pipeline 口径：:perp 腿 qty=合约数（张）；执行器口径：Order.Qty=基础币数量 → ×cs
// （gate quanto/okx ctVal/binance 1.0，缺失 fail-closed——qty 单位断裂曾致名义放大 1/cs 倍）。
// :spot=基础币、MT=手数，原样不换算。
func (p *Pipeline) orderQtyFor(leg LegSpec) (float64, error) {
	if !keydomain.IsType(leg.VenueKey, "perp") {
		return leg.Qty, nil
	}
	if p.csResolver == nil {
		return 0, fmt.Errorf("csResolver not wired (fail-closed)")
	}
	cs, ok := p.csResolver.ContractSize(leg.VenueKey, leg.VenueSymbol)
	if !ok || cs <= 0 {
		return 0, fmt.Errorf("contract size unavailable %s %s (fail-closed)", leg.VenueKey, leg.VenueSymbol)
	}
	return leg.Qty * cs, nil
}

func (p *Pipeline) executeLeg(ctx context.Context, leg LegSpec, clientOrderID string) LegResult {
	exe, ok := p.registry.Get(leg.VenueKey)
	if !ok {
		return LegResult{VenueKey: leg.VenueKey, Status: "FAILED"}
	}
	// D-311 R4：:perp 腿合约数→基础币换算（缺失 fail-closed）。
	orderQty, err := p.orderQtyFor(leg)
	if err != nil {
		log.Printf("[exec] leg %s %s %s: %v", leg.VenueKey, leg.Side, leg.VenueSymbol, err)
		return LegResult{VenueKey: leg.VenueKey, Status: "FAILED"}
	}
	// D-146 S1：MT 腿下单即带 broker 端灾难 SL（原子写入，断连时服务器端兜底）。
	// CEX 腿不挂（CEX 端有仓位生存期+fail-closed）；仅 :mt4/:mt5 腿且配置 >0 时计算。
	var sl float64
	if isMTVenueKey(leg.VenueKey) {
		sl = disasterSL(leg.Side, leg.PriceExpected, p.mtDisasterSlPct)
	}
	fill, err := exe.PlaceOrder(ctx, Order{
		Symbol:        leg.VenueSymbol,
		Side:          leg.Side,
		Qty:           orderQty,
		Venue:         leg.VenueKey,
		StopLoss:      sl,
		ClientOrderID: clientOrderID, // D-195-B §4.3：确定性客户端订单号
	})
	if err != nil {
		// D-421：受理未确认（有 OrderID 未达终态）→ UNKNOWN 带 OrderID（消解凭据，
		// 落库 legs.order_id；禁映 FAILED——假未成交会触发收编平另一腿换腿裸奔）。
		var unconfirmed *ErrFillUnconfirmed
		if errors.As(err, &unconfirmed) {
			log.Printf("[exec] leg %s %s %s: fill unconfirmed order=%s → UNKNOWN", leg.VenueKey, leg.Side, leg.VenueSymbol, unconfirmed.OrderID)
			return LegResult{VenueKey: leg.VenueKey, OrderID: unconfirmed.OrderID, Status: "UNKNOWN"}
		}
		// D-159 W1a R2：腿下单超时/响应丢失 → UNKNOWN（不知成交与否，不能假 FAILED 平仓）。
		// context.DeadlineExceeded/Canceled = 网络超时/取消，订单可能已成交 → 标 UNKNOWN 待人工/对账确认。
		// D-414 §1.6：判定不再限 MT——CEX 超时同样可能已成交，假 FAILED→LEGGED 会让自动收编
		// 平可见腿后换腿裸奔。LEGGED 集合语义钉死为「另一腿确定未成交」=自动收编的安全前提。
		// 其他错误（合约不存在/被拒/零成交）= 确定未成交 → FAILED。
		if isContextTimeoutErr(err) {
			log.Printf("[exec] leg %s %s %s: timeout → UNKNOWN (order may have filled): %v", leg.VenueKey, leg.Side, leg.VenueSymbol, err)
			return LegResult{VenueKey: leg.VenueKey, Status: "UNKNOWN"}
		}
		log.Printf("[exec] leg %s %s %s: %v", leg.VenueKey, leg.Side, leg.VenueSymbol, err)
		return LegResult{VenueKey: leg.VenueKey, Status: "FAILED"}
	}
	// D-108 P2 T2：零成交/被拒 → 腿标 FAILED（不再假 FILLED 幻影腿）。
	// MT5 OrderSend 成功但 lots=0/price=0 = 零成交；CEX 已在 PlaceOrder 内报错返回 err。
	if fill.Qty <= 0 || fill.Price <= 0 {
		log.Printf("[exec] leg %s %s %s: zero fill (qty=%v price=%v) → FAILED", leg.VenueKey, leg.Side, leg.VenueSymbol, fill.Qty, fill.Price)
		return LegResult{VenueKey: leg.VenueKey, OrderID: fill.OrderID, Status: "FAILED"}
	}
	return LegResult{
		VenueKey:   leg.VenueKey,
		OrderID:    fill.OrderID,
		Price:      fill.Price,
		Qty:        fill.Qty,
		Status:     "FILLED",
		Swap:       fill.Swap,
		Commission: fill.Commission,
		FeeUSD:     fill.FeeUSD,
	}
}

// Revalidate 用 QuoteBus 最新报价重算毛收益劣化（§13.1 双阈值；D-051 修③：毛口径比较）。
// 成本侧（佣金/滑点）执行前后不变，净收益劣化完全由毛位移驱动 → 用 originalGrossBps 与重算
// currentBps（spread/mid×10000）同口径比较才正确。返回 true 通过复核，false 劣化超阈值应拒绝。
// D-068 修复：FUNDING_HEDGE 收益来自 funding rate 差不是价格差，价格差近 0 是正常的，
// 用价格差复核会误拒 → FUNDING_HEDGE 只检查报价新鲜度（QuoteBus 有报价即可），跳过价差劣化。
// D-311 R3：卖腿报价挂 canonicalSell 命名空间（XASSET 跨 canonical，调用方回退 canonical）；
// XASSET_CARRY 收益是 carry 年化口径与瞬时价差不可比 → 并入新鲜度免检分支。
func (p *Pipeline) Revalidate(canonical, canonicalSell, venueBuy, venueSell, oppType string, originalGrossBps float64) bool {
	// D-481 普查：此处 canonical/canonicalSell 即采集键——detector 按 venue 侧发射
	//（XASSET：买腿=MtCanonical/卖腿=CexCanonical=symbol+USDT 与采集键 XAUUSDT 同形；
	// CEX 同 canonical 型（FUNDING_HEDGE spot+perp 同所）canonical=采集键；
	// CROSS_VENUE MT 双侧 canonical=symbol map 发射键）。无腿原生 symbol 在手，
	// 由调用方 canonical 形态保证，非错位点。
	qbuy, ok1 := p.qbus.Latest(canonical, venueBuy)
	qsell, ok2 := p.qbus.Latest(canonicalSell, venueSell)
	if !ok1 || !ok2 {
		return false // 报价缺失 = 不通过
	}
	// FUNDING_HEDGE / PERP_FUNDING / XASSET_CARRY：只检查报价新鲜度，跳过价格差劣化
	//（funding rate 差是收益来源，价格差近 0 正常，D-068 修复 FUNDING_HEDGE，
	// D-120 扩展 PERP_FUNDING 同理——gate futures 只有 mark_price 无真实 spread；
	// D-311 R3：XASSET_CARRY 收益口径=carry 年化 pp，与瞬时价差 bps 不可比，同先例免检）
	if oppType == "FUNDING_HEDGE" || oppType == "PERP_FUNDING" || oppType == "XASSET_CARRY" {
		return true
	}
	// CROSS_VENUE：重算瞬时毛 spread
	spread := qsell.Bid - qbuy.Ask
	mid := qbuy.Mid()
	if mid <= 0 {
		return false
	}
	currentBps := spread / mid * 10000
	// 双阈值：max(absBps 绝对, relPct% 相对)（D-077 修复⑪B N4：env 化 ARB_REVALIDATE_ABS_BPS/ARB_REVALIDATE_REL_PCT）
	absThreshold := p.revalidateAbsBps
	relThreshold := math.Abs(originalGrossBps) * p.revalidateRelPct / 100.0
	threshold := math.Max(absThreshold, relThreshold)
	degradation := originalGrossBps - currentBps
	return degradation <= threshold
}
