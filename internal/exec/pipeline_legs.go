package exec

import (
	"context"
	"errors"
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

func (p *Pipeline) executeLeg(ctx context.Context, leg LegSpec) LegResult {
	exe, ok := p.registry.Get(leg.VenueKey)
	if !ok {
		return LegResult{VenueKey: leg.VenueKey, Status: "FAILED"}
	}
	// D-146 S1：MT 腿下单即带 broker 端灾难 SL（原子写入，断连时服务器端兜底）。
	// CEX 腿不挂（CEX 端有仓位生存期+fail-closed）；仅 :mt4/:mt5 腿且配置 >0 时计算。
	var sl float64
	if isMTVenueKey(leg.VenueKey) {
		sl = disasterSL(leg.Side, leg.PriceExpected, p.mtDisasterSlPct)
	}
	fill, err := exe.PlaceOrder(ctx, Order{
		Symbol:   leg.VenueSymbol,
		Side:     leg.Side,
		Qty:      leg.Qty,
		Venue:    leg.VenueKey,
		StopLoss: sl,
	})
	if err != nil {
		// D-159 W1a R2：MT 腿 OrderSend 超时/响应丢失 → UNKNOWN（不知成交与否，不能假 FAILED 平仓）。
		// context.DeadlineExceeded/Canceled = 网络超时/取消，订单可能已成交 → 标 UNKNOWN 待人工/对账确认。
		// 其他错误（合约不存在/被拒/零成交）= 确定未成交 → FAILED。
		if isMTVenueKey(leg.VenueKey) && isContextTimeoutErr(err) {
			log.Printf("[exec] leg %s %s %s: MT timeout → UNKNOWN (order may have filled): %v", leg.VenueKey, leg.Side, leg.VenueSymbol, err)
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
	}
}

// Revalidate 用 QuoteBus 最新报价重算毛收益劣化（§13.1 双阈值；D-051 修③：毛口径比较）。
// 成本侧（佣金/滑点）执行前后不变，净收益劣化完全由毛位移驱动 → 用 originalGrossBps 与重算
// currentBps（spread/mid×10000）同口径比较才正确。返回 true 通过复核，false 劣化超阈值应拒绝。
// D-068 修复：FUNDING_HEDGE 收益来自 funding rate 差不是价格差，价格差近 0 是正常的，
// 用价格差复核会误拒 → FUNDING_HEDGE 只检查报价新鲜度（QuoteBus 有报价即可），跳过价差劣化。
func (p *Pipeline) Revalidate(canonical, venueBuy, venueSell, oppType string, originalGrossBps float64) bool {
	qbuy, ok1 := p.qbus.Latest(canonical, venueBuy)
	qsell, ok2 := p.qbus.Latest(canonical, venueSell)
	if !ok1 || !ok2 {
		return false // 报价缺失 = 不通过
	}
	// FUNDING_HEDGE / PERP_FUNDING：只检查报价新鲜度，跳过价格差劣化
	//（funding rate 差是收益来源，价格差近 0 正常，D-068 修复 FUNDING_HEDGE，
	// D-120 扩展 PERP_FUNDING 同理——gate futures 只有 mark_price 无真实 spread）
	if oppType == "FUNDING_HEDGE" || oppType == "PERP_FUNDING" {
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
