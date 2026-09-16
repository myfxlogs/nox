// D-311 S5：XASSET_CARRY 专属净收入计算——跨 canonical 逐腿泛化（perp=funding、MT=swap）。
// 独立实现，勿照抄 recalcNetFunding 的 longFunding−shortFunding（疑似存量反向=−收入，
// 对盈利仓恒 breach，另列 D-312 待裁）。口径=持有净收入 %/yr：perp BUY=−rate、SELL=+rate；
// MT=swapRateFor 原值（正=收）×3.65 转年化。任一腿缺 fact/不新鲜/映射不可得 → ok=false。
package autotrade

import (
	"context"
	"log"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// recalcXAssetNetCarry 逐腿求和持仓净收入（年化 %）。
// 腿 canonical：BUY→canonicalFromOppID、SELL→canonical_sell（xassetSellCanonical 解析）。
// venue 后缀 :perp → funding fact（LatestFacts KindFunding 按 bare venue 匹配）；
// :mt4/:mt5 → swapRateFor（settled/spec/none，spec 源另过 SwapFactMaxAge 新鲜度）。
// 不假设买=perp——生产反例 XASSET_CARRY:XPTUSD:3001157:mt5:okx:perp=买 MT5 卖 OKX perp。
func (e *Engine) recalcXAssetNetCarry(ctx context.Context, p *pgstore.Position) (float64, bool) {
	legs, err := e.store.ListPositionLegs(ctx, p.ID)
	if err != nil {
		return 0, false
	}
	buyCanonical := canonicalFromOppID(p.OpportunityID)
	if buyCanonical == "" {
		return 0, false
	}
	sellCanonical := e.xassetSellCanonical(ctx, p, legs)
	if sellCanonical == "" {
		return 0, false // canonical_sell + broker_symbol_map 均不可得 → fail-closed
	}
	maxAge := e.cfg.SwapFactMaxAge
	var netCarry float64
	foundAny := false
	for i := range legs {
		leg := legs[i]
		if leg.Phase != "OPEN" {
			continue
		}
		canonical := buyCanonical
		if leg.Side != "BUY" {
			canonical = sellCanonical
		}
		bare := strings.Split(leg.Venue, ":")[0]
		switch {
		case strings.HasSuffix(leg.Venue, ":perp"):
			rate, ts, ok := e.fundingRateForVenue(ctx, bare, canonical)
			if !ok {
				log.Printf("[autotrade] exit pos=%d: funding fact missing venue=%s canonical=%s (fail-closed)", p.ID, bare, canonical)
				return 0, false
			}
			if maxAge > 0 && time.Since(ts) > maxAge {
				log.Printf("[autotrade] exit pos=%d: funding fact stale venue=%s canonical=%s >%v (fail-closed)", p.ID, bare, canonical, maxAge)
				return 0, false
			}
			// 持有净收入：BUY（多头）付 funding=−rate；SELL（空头）收 funding=+rate。
			if leg.Side == "BUY" {
				netCarry -= rate
			} else {
				netCarry += rate
			}
		case strings.HasSuffix(leg.Venue, ":mt5"), strings.HasSuffix(leg.Venue, ":mt4"):
			bps, src := e.swapRateFor(ctx, bare, canonical, leg.Side)
			if src == swapSrcNone {
				log.Printf("[autotrade] exit pos=%d: swapRateFor none venue=%s canonical=%s side=%s (fail-closed)", p.ID, bare, canonical, leg.Side)
				return 0, false
			}
			if src == swapSrcSpec && !e.xassetSpecFactFresh(ctx, bare, canonical, leg.Side) {
				return 0, false
			}
			// swapRateFor 返回 bps/日（正=收）→ ×3.65 转 %/yr。
			netCarry += bps * 3.65
		default:
			log.Printf("[autotrade] exit pos=%d: unknown venue type %s (fail-closed)", p.ID, leg.Venue)
			return 0, false
		}
		foundAny = true
	}
	if !foundAny {
		return 0, false
	}
	log.Printf("[autotrade] exit pos=%d: xasset netCarry=%.4f%% ann", p.ID, netCarry)
	return netCarry, true
}

// fundingRateForVenue 取 (bareVenue, canonical) 最新 funding 年化 %（pct_annualized）。
// LatestFacts(venue="") 返回该 canonical 全 venue 最新 fact，按 bare venue 匹配。
// ok=false = 该 venue 无 fact（fail-closed）。
func (e *Engine) fundingRateForVenue(ctx context.Context, bareVenue, canonical string) (rate float64, ts time.Time, ok bool) {
	facts, err := e.store.LatestFacts(ctx, fact.KindFunding, "", canonical)
	if err != nil {
		return 0, time.Time{}, false
	}
	for _, f := range facts {
		if f.Venue == bareVenue {
			return f.Value, f.Ts, true
		}
	}
	return 0, time.Time{}, false
}

// xassetSpecFactFresh MT 腿 spec 源 swap fact 新鲜度检查（SwapFactMaxAge）。
// swapRateFor 返回 spec 源时调用；任一 spec fact 陈旧 → false（fail-closed 不决策）。
func (e *Engine) xassetSpecFactFresh(ctx context.Context, bare, canonical, side string) bool {
	maxAge := e.cfg.SwapFactMaxAge
	if maxAge <= 0 {
		return true
	}
	kind := fact.KindSwapLong
	if side != "BUY" {
		kind = fact.KindSwapShort
	}
	facts, err := e.store.LatestFacts(ctx, kind, bare, canonical)
	if err != nil || len(facts) == 0 {
		return false
	}
	if time.Since(facts[0].Ts) > maxAge {
		log.Printf("[autotrade] skip exit: spec %s stale venue=%s canonical=%s >%v", kind, bare, canonical, maxAge)
		return false
	}
	return true
}

// xassetSellCanonical 解析卖腿 canonical（D-311 S5）：
// 先 opportunities.canonical_sell（D-300 列）；无行/NULL → 卖腿 venue_symbol 经
// broker_symbol_map 兜底。全不可得 → ""（调用方 fail-closed）。
func (e *Engine) xassetSellCanonical(ctx context.Context, p *pgstore.Position, legs []pgstore.PositionLeg) string {
	if opp, err := e.store.GetOpportunity(ctx, p.OpportunityID); err == nil &&
		opp != nil && opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		return *opp.CanonicalSell
	}
	for i := range legs {
		if legs[i].Phase == "OPEN" && legs[i].Side != "BUY" {
			bare := strings.Split(legs[i].Venue, ":")[0]
			maps, err := e.store.ListSymbolMappings(ctx, bare)
			if err != nil {
				return ""
			}
			for _, m := range maps {
				if m.BrokerSymbol == legs[i].Symbol && m.Canonical != "" {
					return m.Canonical
				}
			}
			return "" // 卖腿映射无行 → fail-closed（不猜 canonical）
		}
	}
	return ""
}
