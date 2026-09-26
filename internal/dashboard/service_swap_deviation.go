// D-182 S4：手动 RPC swap 偏差拦截——ExecuteOpportunityInternal/ExecuteMeanRevertWithCap
// 在 pipeline.Execute 前检查同 venue 已有 OPEN 持仓的 broker 实际 swap vs facts 规格值偏差。
// 与 autotrade.Gate.swapDeviationGateCheck（D-165 S1）同源逻辑，手动入口对齐。
package dashboard

import (
	"context"
	"fmt"
	"math"
	"time"

	"arb/internal/fact"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
	"arb/internal/usdnotional"
)

// swapDeviationBetweenPct D-165 S1/S2 同源纯函数（复制避免跨包依赖，与 gate_checks.go 注释一致）。
// 公式：|facts - actual| / max(|facts|, |actual|, 0.01) × 100。
// 两者为 0→偏差 0（一致）；任一为 0 另一非 0→偏差 100%。
func swapDeviationBetweenPct(facts, actual float64) float64 {
	diff := facts - actual
	if diff < 0 {
		diff = -diff
	}
	maxVal := math.Abs(facts)
	if math.Abs(actual) > maxVal {
		maxVal = math.Abs(actual)
	}
	if maxVal < 0.01 {
		maxVal = 0.01
	}
	return diff / maxVal * 100
}

// checkSwapDeviationGate D-182 S4：手动 RPC swap 偏差拦截。
// 与 autotrade.Gate.swapDeviationGateCheck（D-165 S1）同源逻辑：
//   - 遍历 OPEN 持仓，对每个 OPEN 腿 venue 与 intentBuyBare/intentSellBare 匹配的腿，
//     DailySwapAccrued 取 broker 实际日增量（USD），LatestFacts 取规格值（bps）。
//   - facts 规格值换算 USD/日 = factsBps × notional / 10000（notional 从 legs qty×price×CS 算）。
//   - 偏差率超阈值→返回拒绝原因；空串=放行。
//   - 无匹配持仓/无 DailySwapAccrued/无 facts→放行（首次开仓走 S3）。
//   - 查询失败→fail-closed 拒绝（返回原因）。
//
// legContractSizeOf：MT 腿 CS 换算（s.legContractSizeOf 已有）；nil = CS=1（CEX 腿正确）。
func (s *Service) checkSwapDeviationGate(ctx context.Context, canonical, venueBuy, venueSell string) string {
	if s.swapDeviationGatePct <= 0 {
		return "" // 禁用
	}
	intentBuyBare := keydomain.BareOf(venueBuy)
	intentSellBare := keydomain.BareOf(venueSell)

	// 取 facts 规格值（同 recalcNetSwap 逻辑）
	longFacts, err := s.store.LatestFacts(ctx, fact.KindSwapLong, "", canonical)
	if err != nil {
		return fmt.Sprintf("swap deviation gate: long facts query failed (%v)", err)
	}
	shortFacts, err := s.store.LatestFacts(ctx, fact.KindSwapShort, "", canonical)
	if err != nil {
		return fmt.Sprintf("swap deviation gate: short facts query failed (%v)", err)
	}

	// 列 OPEN 持仓
	positions, err := s.store.ListPositions(ctx, "OPEN", 50, 0)
	if err != nil {
		return fmt.Sprintf("swap deviation gate: list open positions failed (%v)", err)
	}
	for _, p := range positions {
		legs, err := s.store.ListPositionLegs(ctx, p.ID)
		if err != nil {
			return fmt.Sprintf("swap deviation gate: list legs pos=%d failed (%v)", p.ID, err)
		}
		for _, leg := range legs {
			if leg.Phase != "OPEN" {
				continue
			}
			legBare := keydomain.BareOf(leg.Venue)
			if legBare != intentBuyBare && legBare != intentSellBare {
				continue
			}
			// D-189 P0-1：快照查询用完整 venueKey（leg.Venue），legBare 仅用于 intent venue 匹配与 facts 匹配。
			actualUSD, ok, err := s.store.DailySwapAccrued(ctx, p.ID, leg.Venue, time.Now().UTC())
			if err != nil {
				return fmt.Sprintf("swap deviation gate: daily swap accrued pos=%d venue=%s failed (%v)", p.ID, leg.Venue, err)
			}
			if !ok || actualUSD == 0 {
				continue
			}
			var factsBps float64
			if leg.Side == "BUY" {
				for _, f := range longFacts {
					if f.Venue == legBare {
						factsBps = f.Value
						break
					}
				}
			} else {
				for _, f := range shortFacts {
					if f.Venue == legBare {
						factsBps = f.Value
						break
					}
				}
			}
			// D-190：notional 改 usdnotional.PerLot（USD 统一换算，JPY 对 ÷ USDJPY）。
			// cs 仍用 s.legContractSize，nil→CS=1 回退（现状语义）。
			price := 0.0
			if leg.PriceActual != nil {
				price = *leg.PriceActual
			}
			cs := 1.0
			if s.legContractSize != nil {
				if resolved, ok := s.legContractSize(leg.Venue, leg.Symbol); ok && resolved > 0 {
					cs = resolved
				}
			}
			perLot, ok := usdnotional.PerLot(canonical, price, cs, s.qbus)
			if !ok || perLot <= 0 {
				continue // 无法算 USD notional→跳过（走 S3，fail-closed）
			}
			// D-311 R6：EffectiveQty=成交口径（CEX=基础币 cs=1 两时代全对、MT=手数）。
			notional := leg.EffectiveQty() * perLot
			factsUSD := factsBps * notional / 10000
			deviationPct := swapDeviationBetweenPct(factsUSD, actualUSD)
			if deviationPct > s.swapDeviationGatePct {
				return fmt.Sprintf("swap deviation gate: venue=%s deviation=%.1f%% > %.1f%% (broker actual=$%.4f/day vs facts spec=$%.4f/day)",
					legBare, deviationPct, s.swapDeviationGatePct, actualUSD, factsUSD)
			}
		}
	}
	return ""
}

// SetSwapDeviationGatePct D-182 S4：注入 swap 偏差拦截阈值（手动入口同源）。
// ≤0 = 禁用（默认，回退旧行为）。main 从 cfg.SwapDeviationGatePct 注入。
func (s *Service) SetSwapDeviationGatePct(pct float64) { s.swapDeviationGatePct = pct }

// 占位：确保 pgstore import 被引用（leg.PriceActual 类型来自 pgstore.PositionLeg）。
var _ = pgstore.PositionLeg{}
