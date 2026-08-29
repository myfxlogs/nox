package dashboard

// 持仓 enrich 工具函数：持有天数 + canonical 解析 + 收敛型 bps 重算。

import (
	"time"

	"arb/internal/bus"
	"arb/internal/combo"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// holdDaysToNow (now − opened_at)/24h；opened_at NULL → min(OPEN FILLED 腿 ts)。
// 与 settlement.computeHoldDays 同构（终点=now 非 closed_at；活跃行持有至今）。
func holdDaysToNow(pb *v1.Position, openLegs []pgstore.PositionLeg, now time.Time) float64 {
	var openedAt time.Time
	if pb.OpenedAt != nil {
		openedAt = pb.OpenedAt.AsTime()
	} else {
		var minTs time.Time
		for _, l := range openLegs {
			if l.Status == "FILLED" {
				if minTs.IsZero() || l.Ts.Before(minTs) {
					minTs = l.Ts
				}
			}
		}
		if !minTs.IsZero() {
			openedAt = minTs
		}
	}
	if openedAt.IsZero() {
		return 0
	}
	return now.Sub(openedAt).Hours() / 24
}

// perpFundingExpectedUSD D-146 S5 微件②：PERP_FUNDING 年化 expected_net_bps 按持有天数折算应得 USD。
// expected_net_bps 为年化百分点（5.0=5%，evaluator.go:85-87）→ expected_usd = (bps/100)×notional×(holdDays/365)。
// CEX funding 24/7 计息（8h×3/天，含周末）→ 用 calendar holdDays 非 settleDays。
// 任一因子 ≤0 → 0（诚实缺省，防伪造）。
func perpFundingExpectedUSD(expectedNetBps, notionalUSD, holdDays float64) float64 {
	if expectedNetBps <= 0 || notionalUSD <= 0 || holdDays <= 0 {
		return 0
	}
	return expectedNetBps / 100 * notionalUSD * holdDays / 365
}

// canonicalFromOppID 从 opportunity_id 解析 canonical（格式 PREFIX:SYMBOL:...）。
// 与 settlement/autotrade 同构纯函数（本包不依赖 autotrade 避免环依赖；combo.FromOppID 已引入）。
func canonicalFromOppID(oppID string) string {
	ck := combo.FromOppID(oppID) // 剥离旧 tick 尾时间戳
	parts := splitColon(ck)
	if len(parts) >= 2 {
		return parts[1]
	}
	return ""
}

// splitColon 按冒号分割（combo.FromOppID 已处理 venue 含 ":" 的边界，此处 ck 已是组合键）。
func splitColon(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// computeCurrentBps 用实时报价重算收敛型当前 bps（D-111）。
// CROSS_VENUE: net_bps = (sell.Bid - buy.Ask) / mid * 10000 - (commissionBps + slippageCost) * 2
// MEANREVERT: bps = spread / ask * 10000（gross，无成本扣除）
// 报价缺失/异常 → ok=false（fail-closed，不回退 rollup）。
// 公式与 detector.tryCross / evaluator.Evaluate 同构（P3 单一真相源——公式简单不值得抽取，注释指认）。
func computeCurrentBps(pb *v1.Position, openLegs []pgstore.PositionLeg, qbus *bus.QuoteBus, commissionFor func(string) float64, slippageBps float64) (float64, bool) {
	canonical := canonicalFromOppID(pb.OpportunityId)
	if canonical == "" || qbus == nil || len(openLegs) < 2 {
		return 0, false
	}
	// 找 BUY 腿和 SELL 腿
	var buyLeg, sellLeg *pgstore.PositionLeg
	for i := range openLegs {
		if openLegs[i].Phase != "OPEN" {
			continue
		}
		switch openLegs[i].Side {
		case "BUY":
			buyLeg = &openLegs[i]
		case "SELL":
			sellLeg = &openLegs[i]
		}
	}
	if buyLeg == nil || sellLeg == nil {
		return 0, false
	}
	// 取当前报价
	qBuy, okBuy := qbus.Latest(canonical, buyLeg.Venue)
	qSell, okSell := qbus.Latest(canonical, sellLeg.Venue)
	if !okBuy || !okSell {
		return 0, false
	}
	switch pb.Type {
	case "CROSS_VENUE":
		spread := qSell.Bid - qBuy.Ask
		if spread <= 0 {
			return 0, false
		}
		mid := qBuy.Mid()
		if mid <= 0 {
			return 0, false
		}
		grossBps := spread / mid * 10000
		commissionBps := 0.0
		if commissionFor != nil {
			commissionBps = commissionFor(buyLeg.Venue) + commissionFor(sellLeg.Venue)
		}
		slippageCost := slippageBps * 2
		netBps := grossBps - (commissionBps+slippageCost)*2
		return netBps, true
	case "MEANREVERT":
		spread := qSell.Bid - qBuy.Ask
		if spread <= 0 {
			return 0, false
		}
		if qBuy.Ask <= 0 {
			return 0, false
		}
		bps := spread / qBuy.Ask * 10000
		return bps, true
	default:
		return 0, false
	}
}
