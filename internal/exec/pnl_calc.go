package exec

import (
	"context"
	"fmt"
	"log"
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
// CS：仅 MT 腿经 csResolver 查服务器规格（qty=手数，修掉漏 CS 的 100 倍错误）；
// CEX 腿 cs=1.0——EffectiveQty=filled_qty 已是基础币，×ct_val=二次换算
// （D-311 R5 缺陷#6a：okx TRX qty=2950 基础币 ×ct_val 1000 曾放千倍；
// R6：qty 列=LegSpec 请求量含合约数时代，实际持仓量取 EffectiveQty）。
// fx：计价币→USD 换算（D-105 T2）；fx=0（缺失）→ PnL=0（宁缺勿虚增，调用方发 critical alert）。
// 开仓价缺失 → PnL=0（不猜，留对账纠偏）。
func (p *Pipeline) calcLegPnL(leg pgstore.PositionLeg, fill Fill, fx float64) float64 {
	// D-311 R6：EffectiveQty=成交口径——CEX=基础币（cs=1.0 两时代全对）、MT=手数（×cs）。
	return p.legPricePnL(leg, fill.Price, leg.EffectiveQty(), fx)
}

// legPricePnL 价格差 PnL 核（D-408 提取共享：新平腿 fill 价与持久化 CLOSE 腿价同公式）。
// 开仓价缺失 → 0（不猜，留对账纠偏）；fx=0 → 0（宁缺勿虚增，调用方发 critical alert）。
// D-159 W3a R4：只算价格差 PnL，swap/commission 走 cashflows（双计拆除不动）。
func (p *Pipeline) legPricePnL(leg pgstore.PositionLeg, closePrice, qty, fx float64) float64 {
	if leg.PriceActual == nil {
		return 0
	}
	cs := 1.0
	if p.csResolver != nil && isMTVenueKey(leg.Venue) {
		if v, ok := p.csResolver.ContractSize(leg.Venue, leg.Symbol); ok && v > 0 {
			cs = v
		}
	}
	dir := 1.0
	if leg.Side == "SELL" {
		dir = -1.0
	}
	return (closePrice - *leg.PriceActual) * qty * cs * dir * fx
}

// sumClosedLegsPnL D-408 S1：realized_pnl=持久化 CLOSE 腿汇总（单一事实源=落库腿数据，
// 初平/重试/回填三路径统一——循环内累加会漏前次已平腿与回填腿，498 实证 −1.42→−12.36）。
// 每个 OPEN+FILLED 腿的全部 CLOSE FILLED/EXTERNAL 腿按各自 FilledQty 计（跨尝试部分成交
// 天然覆盖，D-311 R6 同单位；D-015 R1：EXTERNAL=cex_private_events 平仓帧证据链认定的
// 「已平」事实，价缺则同 FILLED 缺价处理）；CLOSE 腿缺 PriceActual → 该腿计 0 + critical
// alert（宁缺勿虚增）；无 CLOSE 腿的 OPEN 腿不累计（残腿校验随后拦阻，不静默虚增）。
func (p *Pipeline) sumClosedLegsPnL(ctx context.Context, posID int64, legs, closeLegs []pgstore.PositionLeg, fx float64) float64 {
	closesByLegNo := make(map[int16][]pgstore.PositionLeg)
	for _, cl := range closeLegs {
		if cl.Status == "FILLED" || cl.Status == "EXTERNAL" {
			closesByLegNo[cl.LegNo] = append(closesByLegNo[cl.LegNo], cl)
		}
	}
	var total float64
	for _, leg := range legs {
		if leg.Phase != "OPEN" || leg.Status != "FILLED" {
			continue
		}
		for _, cl := range closesByLegNo[leg.LegNo] {
			if cl.PriceActual == nil || *cl.PriceActual <= 0 {
				if err := p.store.InsertPositionAlert(ctx, posID, "critical",
					fmt.Sprintf("持仓 #%d 第 %d 腿平仓缺成交价，盈亏按 0 计（宁缺勿虚增）", posID, leg.LegNo)); err != nil {
					log.Printf("[exec] pos %d alert write: %v", posID, err)
				}
				continue
			}
			qty := leg.EffectiveQty()
			if cl.FilledQty != nil && *cl.FilledQty > 0 {
				qty = *cl.FilledQty
			}
			total += p.legPricePnL(leg, *cl.PriceActual, qty, fx)
		}
	}
	return total
}

// calibratePnL 回读校准：按 ticket 查历史单权威 PnL（D-077 修复⑪A M1 回读校准）。
// 命中 HistoryOrder.Profit 时以其为权威覆盖同步路径公式结果。
// D-159 W3a R4：权威值只用价格 PnL（Profit），Swap/Commission 走 cashflows（与 calcLegPnL 双计拆除对齐）。
// D-105 T2：权威值 × fx（同一 canonical/fxResolver，计价币→USD）。
// D-159 W3a R4 + D-422 混合校准：应校准腿 = OPEN+FILLED+有 OrderID+venue 有历史查证器
// （无查证器的 CEX 腿不计入——其公式价 PnL 并入总值，非估算）；可校准腿全部命中才 ok=true，
// 有 miss → est=true（调用方写 pnl_estimated）；无可校准腿 → ok=false/est=false（纯 CEX 不写 note）。
func (p *Pipeline) legCalibratable(venue string) bool {
	if p.pnlCalibrator == nil {
		return false
	}
	// D-422：可校准性=该 venue 有历史查证器（reconcile 路由；calibrator 未暴露路由接口
	// 时回退 isMTVenueKey——CEX 腿不再被计入应校准腿数）。
	if hr, ok := p.pnlCalibrator.(interface {
		HasHistoryQuerier(venue string) bool
	}); ok {
		return hr.HasHistoryQuerier(venue)
	}
	return isMTVenueKey(venue)
}

func (p *Pipeline) calibratePnL(ctx context.Context, legs, closeLegs []pgstore.PositionLeg, fx float64) (auth float64, ok bool, est bool) {
	if p.pnlCalibrator == nil {
		return 0, false, false
	}
	closesByLegNo := make(map[int16][]pgstore.PositionLeg)
	for _, cl := range closeLegs {
		if cl.Status == "FILLED" {
			closesByLegNo[cl.LegNo] = append(closesByLegNo[cl.LegNo], cl)
		}
	}
	var totalPnL float64
	should, calibrated := 0, 0
	for _, leg := range legs {
		if leg.Phase != "OPEN" || leg.Status != "FILLED" || leg.OrderID == "" {
			continue
		}
		if !p.legCalibratable(leg.Venue) {
			// D-422：CEX 等无查证器腿走公式价 PnL（与 sumClosedLegsPnL 同口径——
			// 对该 venue 而言公式价即权威，非估算）。
			for _, cl := range closesByLegNo[leg.LegNo] {
				if cl.PriceActual == nil {
					continue
				}
				qty := leg.EffectiveQty()
				if cl.FilledQty != nil && *cl.FilledQty > 0 {
					qty = *cl.FilledQty
				}
				totalPnL += p.legPricePnL(leg, *cl.PriceActual, qty, fx)
			}
			continue
		}
		should++
		vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		hp, err := p.pnlCalibrator.QueryHistoryPnL(vctx, leg.Venue, leg.OrderID, 3)
		cancel()
		if err != nil || hp == nil {
			continue
		}
		// D-159 W3a R4：权威值只用价格 PnL（Profit）；Swap/Commission 由 cashflows 组件承载。
		totalPnL += hp.Profit * fx
		calibrated++
	}
	if should == 0 {
		return 0, false, false
	}
	if calibrated != should {
		return totalPnL, false, true
	}
	return totalPnL, true, false
}
