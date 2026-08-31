package autotrade

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"arb/internal/fact"
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
func (g *Gate) environmentIsRealCheck(ctx context.Context, intent TradeIntent) GateVerdict {
	wantLive := g.cfg.Environment == "live"
	for _, venueKey := range []string{intent.VenueBuy, intent.VenueSell} {
		venue, _, _ := strings.Cut(venueKey, ":")
		if venue == "" {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("environment check: empty venue in %s", venueKey)}
		}
		acct, err := g.store.GetBrokerAccount(ctx, venue)
		if err != nil {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("environment check: venue %s not found (%v)", venue, err)}
		}
		if acct.IsReal == nil {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("environment check: venue %s is_real unknown (null)", venue)}
		}
		isLive := *acct.IsReal
		if isLive != wantLive {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("environment check: venue %s is_real=%v != environment=%s", venue, isLive, g.cfg.Environment)}
		}
	}
	return GateVerdict{Pass: true}
}

// liveGateCheck D-159 W6a S2：LIVE 人工开关检查。
// LiveTrade=false 时，任一腿 is_real=true → 拒绝（实盘账户不允许开仓）。
// is_real=false（demo）→ 放行。is_real=nil（未知）→ fail-closed 拒绝。
// venue 不存在 → fail-closed 拒绝。
func (g *Gate) liveGateCheck(ctx context.Context, intent TradeIntent) GateVerdict {
	for _, venueKey := range []string{intent.VenueBuy, intent.VenueSell} {
		venue, _, _ := strings.Cut(venueKey, ":")
		if venue == "" {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("live gate: empty venue in %s", venueKey)}
		}
		acct, err := g.store.GetBrokerAccount(ctx, venue)
		if err != nil {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("live gate: venue %s not found (%v)", venue, err)}
		}
		if acct.IsReal == nil {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("live gate: venue %s is_real unknown (null)", venue)}
		}
		if *acct.IsReal {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("live trade disabled (ARB_LIVE_TRADE=false, venue=%s is_real=true)", venue)}
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
	intentBuyBare, _, _ := strings.Cut(intent.VenueBuy, ":")
	intentSellBare, _, _ := strings.Cut(intent.VenueSell, ":")
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
			legBare, _, _ := strings.Cut(leg.Venue, ":")
			// 只检查与 intent venue 匹配的腿
			if legBare != intentBuyBare && legBare != intentSellBare {
				continue
			}
			// 取 broker 实际 swap 日增量
			actualUSD, ok, err := g.store.DailySwapAccrued(ctx, p.ID, legBare, time.Now().UTC())
			if err != nil {
				return GateVerdict{Pass: false, Reason: fmt.Sprintf("swap deviation gate: daily swap accrued pos=%d venue=%s failed (%v)", p.ID, legBare, err)}
			}
			if !ok || actualUSD == 0 {
				continue // 无快照或零增量→跳过（走 S3）
			}
			// 取 facts 规格值（bps）
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
			// facts 换算 USD/日 = bps × notional / 10000
			notional := leg.Qty
			if leg.PriceActual != nil {
				notional *= *leg.PriceActual
			}
			// D-165.1：补 ContractSize 乘法（MT 腿 CS≠1 时 notional 少算 CS 倍）。
			// resolver 未注入（nil）→ 用 CS=1（回退，CEX 腿正确）；注入但 ok=false → 跳过该腿。
			if g.cfg.LegContractSizeResolver != nil {
				cs, ok := g.cfg.LegContractSizeResolver(leg.Venue, leg.Symbol)
				if !ok || cs <= 0 {
					continue // 无法算 notional→跳过（走 S3）
				}
				notional *= cs
			}
			if notional <= 0 {
				continue // 无法算 notional→跳过
			}
			factsUSD := factsBps * notional / 10000
			// 偏差率
			deviationPct := swapDeviationBetween(factsUSD, actualUSD)
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
func swapDeviationBetween(facts, actual float64) float64 {
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
