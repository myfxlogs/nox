package gate

import (
	"context"
	"fmt"
	"math"
	"time"

	"arb/internal/data/fact"
	"arb/internal/data/keydomain"
	"arb/internal/platform/usdnotional"
	"arb/internal/store/pgstore"
)

// environmentIsRealCheck D-159 W1a I6：environment/is_real 一致性检查。
// 校验 intent 双腿 venue 的 broker_accounts.is_real 与 g.cfg.Environment 一致：
//   - is_real=true ↔ environment='live'
//   - is_real=false ↔ environment='demo'
//   - is_real=nil（未知）→ fail-closed 拒绝
//   - venue 不存在 → fail-closed 拒绝
//
// venueKey 格式 "account:mt5"，venue=account（broker_accounts.venue）。
// GetBrokerAccount → ListVenueIsReal 合并视图（broker_accounts.is_real ∪
// exchange_configs.simulated 取反）——CEX venue（gate/okx/binance）开始可判；MT 语义不变。
func (g *Gate) environmentIsRealCheck(ctx context.Context, intent TradeIntent) GateVerdict {
	wantLive := g.cfg.Environment == "live"
	isRealMap, err := g.store.ListVenueIsReal(ctx)
	if err != nil {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("environment check: is_real map query failed (%v)", err)}
	}
	for _, venueKey := range []string{intent.VenueBuy, intent.VenueSell} {
		// venueKey 全键直查（venue:type 粒度；裸 venue 查询吃漂移=安全洞）
		if venueKey == "" {
			return GateVerdict{Pass: false, Reason: "environment check: empty venueKey"}
		}
		isReal, known := isRealMap[venueKey]
		if !known {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("environment check: venue %s not found", venueKey)}
		}
		if isReal == nil {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("environment check: venue %s is_real unknown (null)", venueKey)}
		}
		isLive := *isReal
		if isLive != wantLive {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("environment check: venue %s is_real=%v != environment=%s", venueKey, isLive, g.cfg.Environment)}
		}
	}
	return GateVerdict{Pass: true}
}

// liveGateCheck D-159 W6a S2：LIVE 人工开关检查。
// LiveTrade=false 时，任一腿 is_real=true → 拒绝（实盘账户不允许开仓）。
// is_real=false（demo）→ 放行。is_real=nil（未知）→ fail-closed 拒绝。
// venue 不存在 → fail-closed 拒绝。
// 同 environmentIsRealCheck 切 ListVenueIsReal 合并视图（CEX venue 可判）。
func (g *Gate) liveGateCheck(ctx context.Context, intent TradeIntent) GateVerdict {
	isRealMap, err := g.store.ListVenueIsReal(ctx)
	if err != nil {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("live gate: is_real map query failed (%v)", err)}
	}
	for _, venueKey := range []string{intent.VenueBuy, intent.VenueSell} {
		// venueKey 全键直查
		if venueKey == "" {
			return GateVerdict{Pass: false, Reason: "live gate: empty venueKey"}
		}
		isReal, known := isRealMap[venueKey]
		if !known {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("live gate: venue %s not found", venueKey)}
		}
		if isReal == nil {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("live gate: venue %s is_real unknown (null)", venueKey)}
		}
		if *isReal {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("live trade disabled (ARB_LIVE_TRADE=false, venueKey=%s is_real=true)", venueKey)}
		}
	}
	return GateVerdict{Pass: true}
}

// swapDeviationGateCheck D-165 S1：swap 偏差拦截——同 venue 已有 OPEN 持仓的 broker 实际 swap
// vs facts 规格值偏差超阈值→拒绝新开仓。
// 逻辑：
//   - 遍历 activePositions，对每个 OPEN 持仓 ListPositionLegs 取 OPEN 腿 venue。
//   - 腿 venue 与 intent.VenueBuy/VenueSell 的 bare venue 匹配→该持仓是"同 venue 历史持仓"。
//   - 对匹配持仓的 venue，DailySwapAccrued 取 broker 实际日增量（USD），LatestFacts 取规格值（bps）。
//   - facts 规格值换算 USD/日 = factsBps × notional / 10000（notional 从 legs qty×price×CS 算）。
//   - 用 swapDeviationBetween 纯函数算偏差率，超阈值→拒绝。
//   - 无匹配持仓/无 DailySwapAccrued/无 facts→放行（首次开仓走 S3）。
//   - 查询失败→fail-closed 拒绝。
func (g *Gate) swapDeviationGateCheck(ctx context.Context, intent TradeIntent, activePositions []pgstore.Position) GateVerdict {
	intentBuyBare := keydomain.BareOf(intent.VenueBuy)
	intentSellBare := keydomain.BareOf(intent.VenueSell)
	canonical := intent.Canonical

	// 取 facts 规格值（同 recalcNetSwap 逻辑）
	longFacts, err := g.store.LatestFacts(ctx, fact.KindSwapLong, "", canonical)
	if err != nil {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("swap deviation gate: long facts query failed (%v)", err)}
	}
	shortFacts, err := g.store.LatestFacts(ctx, fact.KindSwapShort, "", canonical)
	if err != nil {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("swap deviation gate: short facts query failed (%v)", err)}
	}

	for _, p := range activePositions {
		legs, err := g.store.ListPositionLegs(ctx, p.ID)
		if err != nil {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("swap deviation gate: list legs pos=%d failed (%v)", p.ID, err)}
		}
		for _, leg := range legs {
			if leg.Phase != "OPEN" {
				continue
			}
			legBare := keydomain.BareOf(leg.Venue)
			// 只检查与 intent venue 匹配的腿
			if legBare != intentBuyBare && legBare != intentSellBare {
				continue
			}
			// 取 broker 实际 swap 日增量
			// 快照查询用完整 venueKey（leg.Venue），legBare 仅用于 intent venue 匹配与 facts 匹配。
			actualUSD, ok, err := g.store.DailySwapAccrued(ctx, p.ID, leg.Venue, time.Now().UTC())
			if err != nil {
				return GateVerdict{Pass: false, Reason: fmt.Sprintf("swap deviation gate: daily swap accrued pos=%d venue=%s failed (%v)", p.ID, leg.Venue, err)}
			}
			if !ok || actualUSD == 0 {
				continue // 无快照或零增量→跳过（走 S3）
			}
			// 取 facts 规格值（bps）——D-565 S5：超龄行不采（SwapFactMaxAge 同 discover/exit 龄闸）。
			factFresh := func(f fact.Fact) bool {
				return g.cfg.SwapFactMaxAge <= 0 || time.Since(f.Ts) <= g.cfg.SwapFactMaxAge
			}
			var factsBps float64
			if leg.Side == "BUY" {
				for _, f := range longFacts {
					if f.Venue == legBare && factFresh(f) {
						factsBps = f.Value
						break
					}
				}
			} else {
				for _, f := range shortFacts {
					if f.Venue == legBare && factFresh(f) {
						factsBps = f.Value
						break
					}
				}
			}
			// facts 换算 USD/日 = bps × notional / 10000
			// notional 改 usdnotional.PerLot（USD 统一换算，JPY 对 ÷ USDJPY）。
			// canonical 用函数顶部 intent.Canonical 变量（勿用 position.Canonical）。
			// cs=1.0 静默回退删——resolver 缺/返非正→走「无法评估」同径 fail-closed。
			price := 0.0
			if leg.PriceActual != nil {
				price = *leg.PriceActual
			}
			cs := 0.0
			if g.cfg.LegContractSizeResolver != nil {
				if resolved, ok := g.cfg.LegContractSizeResolver(leg.Venue, leg.Symbol); ok && resolved > 0 {
					cs = resolved
				}
			}
			if cs <= 0 {
				continue // 合约规格不可得→无法评估（fail-closed 同径，不编 cs=1）
			}
			perLot, ok := usdnotional.PerLot(canonical, price, cs, g.cfg.Quotes)
			if !ok || perLot <= 0 {
				continue // 无法算 USD notional→跳过（走 S3，fail-closed）
			}
			notional := leg.EffectiveQty() * perLot // D-311 R6：成交口径（CEX=基础币）
			factsUSD := factsBps * notional / 10000
			// 偏差率
			deviationPct := SwapDeviationBetween(factsUSD, actualUSD)
			if deviationPct > g.cfg.SwapDeviationGatePct {
				return GateVerdict{Pass: false, Reason: fmt.Sprintf("swap deviation gate: venue=%s deviation=%.1f%% > %.1f%% (broker actual=$%.4f/day vs facts spec=$%.4f/day)",
					legBare, deviationPct, g.cfg.SwapDeviationGatePct, actualUSD, factsUSD)}
			}
		}
	}
	return GateVerdict{Pass: true}
}

// swapDeviationBetween D-165 S1/S2：偏差率纯函数（复用 swapledger.reconcileLeg 公式，但独立实现避免跨包依赖）。
// 公式：|facts - actual| / max(|facts|, |actual|, 0.01) × 100。
// 两者为 0→偏差 0（一致）；任一为 0 另一非 0→偏差 100%。
func SwapDeviationBetween(facts, actual float64) float64 {
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
