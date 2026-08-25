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
	return (priceDiff + fill.Swap - fill.Commission) * fx
}

// calibratePnL 回读校准：按 ticket 查历史单权威 PnL（D-077 修复⑪A M1 回读校准）。
// 命中 HistoryOrder.Profit 时以其为权威覆盖同步路径公式结果。
// D-105 T2：权威值 × fx（同一 canonical/fxResolver，计价币→USD）。
// calibrator 未接线/未找到 → 返回 ok=false（用同步路径公式结果）。
func (p *Pipeline) calibratePnL(ctx context.Context, legs []pgstore.PositionLeg, fx float64) (float64, bool) {
	if p.pnlCalibrator == nil {
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
		// 权威 PnL = Profit + Swap − Commission（与 auto_close.go:99 真值路径统一，P3）
		totalPnL += hp.Profit + hp.Swap - hp.Commission
		calibrated++
	}
	if calibrated == 0 {
		return 0, false
	}
	return totalPnL * fx, true
}
