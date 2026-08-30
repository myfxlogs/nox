package exec

import (
	"context"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// canonicalFromOppID 从 opportunity_id 解析 canonical（格式 PREFIX:SYMBOL:...）。
// D-105 T2：与 autotrade.canonicalFromOppID 同款（exec 不依赖 autotrade，本地复刻）。
func canonicalFromOppID(oppID string) string {
	parts := strings.Split(oppID, ":")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// calcLegPnL 同步路径计算单腿平仓 PnL（D-077 修复⑪A M1）。
// 公式：((Fill.Price − 开仓价) × qty × CS × 方向 + Fill.Swap − Fill.Commission) × fx
// CS：MT 腿经 csResolver 查服务器规格（修掉漏 CS 的 100 倍错误）；CEX 腿 CS=1。
// fx：计价币→USD 换算（D-105 T2）；fx=0（缺失）→ PnL=0（宁缺勿虚增，调用方发 critical alert）。
// 开仓价缺失 → PnL=0（不猜，留对账纠偏）。
func (p *Pipeline) calcLegPnL(leg pgstore.PositionLeg, fill Fill, fx float64) float64 {
	if leg.PriceActual == nil {
		return 0
	}
	cs := 1.0
	if p.csResolver != nil {
		if v, ok := p.csResolver.ContractSize(leg.Venue, leg.Symbol); ok && v > 0 {
			cs = v
		}
	}
	dir := 1.0
	if leg.Side == "SELL" {
		dir = -1.0
	}
	priceDiff := (fill.Price - *leg.PriceActual) * leg.Qty * cs * dir
	// D-159 W3a R4：双计拆除——calcLegPnL 只算价格差 PnL，swap/commission 走 cashflows
	//（pipeline_legs.go 平仓写入）。settlement netPnl = realized_pnl（价格差）+ cashflowsNet（组件）
	// = 净值一次合成（原 realized 含 swap/commission 再加 cashflowsNet = 双计）。
	return priceDiff * fx
}

// calibratePnL 回读校准：按 ticket 查历史单权威 PnL（D-077 修复⑪A M1 回读校准）。
// 命中 HistoryOrder.Profit 时以其为权威覆盖同步路径公式结果。
// D-159 W3a R4：权威值只用价格 PnL（Profit），Swap/Commission 走 cashflows（与 calcLegPnL 双计拆除对齐）。
// D-105 T2：权威值 × fx（同一 canonical/fxResolver，计价币→USD）。
// D-159 W3a R4 校准规则：应校准腿总数 = OPEN+FILLED+有 OrderID 的腿；
// 所有应校准腿全部成功才返回 ok=true（部分成功不覆盖整仓，R4）。
// calibrator 未接线/未全部命中 → 返回 ok=false（用同步路径公式结果）。
func (p *Pipeline) calibratePnL(ctx context.Context, legs []pgstore.PositionLeg, fx float64) (float64, bool) {
	if p.pnlCalibrator == nil {
		return 0, false
	}
	shouldCalibrate := 0
	for _, leg := range legs {
		if leg.Phase == "OPEN" && leg.Status == "FILLED" && leg.OrderID != "" {
			shouldCalibrate++
		}
	}
	if shouldCalibrate == 0 {
		return 0, false
	}
	var totalPnL float64
	calibrated := 0
	for _, leg := range legs {
		if leg.Phase != "OPEN" || leg.Status != "FILLED" || leg.OrderID == "" {
			continue
		}
		vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		hp, err := p.pnlCalibrator.QueryHistoryPnL(vctx, leg.Venue, leg.OrderID, 3)
		cancel()
		if err != nil || hp == nil {
			continue
		}
		// D-159 W3a R4：权威值只用价格 PnL（Profit）；Swap/Commission 由 cashflows 组件承载。
		totalPnL += hp.Profit
		calibrated++
	}
	// D-159 W3a R4：全部应校准腿成功才返回 true（部分成功不覆盖整仓）。
	if calibrated != shouldCalibrate {
		return 0, false
	}
	return totalPnL * fx, true
}
