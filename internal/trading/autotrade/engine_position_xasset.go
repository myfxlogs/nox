// XASSET_CARRY 专属净收入计算——跨 canonical 逐腿泛化（perp=funding、MT=swap）。
// 独立实现，勿照抄 recalcNetFunding 的 longFunding−shortFunding（疑似存量反向=−收入，
// 对盈利仓恒 breach，另列 D-312 待裁）。口径=持有净收入 %/yr：perp BUY=−rate、SELL=+rate；
// MT=swapRateFor 原值（正=收）×3.65 转年化。任一腿缺 fact/不新鲜/映射不可得 → ok=false。
package autotrade

import (
	"context"
	"log"
	"nox/internal/data/keydomain"
	"time"

	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
)

// carry 期望口径常量（D-027 裁定）：econ 入场闸/收割地平/轮转 EV=滚动 7 日均值年化；
// carry_death breach 量=滚动 3 日均值；展示路径 window≤0=瞬时（同 fundingRateForVenue）。
var (
	carryEconWindow      = fact.FundingMeanWindow // D-034：窗口常量单源（7d）
	carryDeathMeanWindow = 3 * 24 * time.Hour
)

// recalcXAssetNetCarry 逐腿求和持仓净收入（年化 %）。
// 腿 canonical：BUY→canonicalFromOppID、SELL→canonical_sell（xassetSellCanonical 解析）。
// venue 后缀 :perp → funding fact（window>0=7d/3d 滚动均值，否则瞬时最新值）；
// :mt4/:mt5 → swapRateFor（settled/spec/none；window>0 时 spec 源同窗均值）。
// 不假设买=perp——生产反例 XASSET_CARRY:XPTUSD:3001157:mt5:okx:perp=买 MT5 卖 OKX perp。
// src=调用源日志前缀（D-131：评估径 "exit pos=" vs 展示径 "carry show pos=" 分标——
// 同文日志曾使展示径每秒刷屏被误读为权威评估在跑）。
func (e *Engine) recalcXAssetNetCarry(ctx context.Context, p *pgstore.Position, window time.Duration, src string) (float64, bool) {
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
		// 腿级 carry 提取共享 helper（替代组合假想腿同函数族算，不双标）。
		legCarry, ok := e.xassetLegCarryAnnual(ctx, leg.Venue, canonical, leg.Side, maxAge, window)
		if !ok {
			log.Printf("[autotrade] %s pos=%d: leg carry fail-closed venue=%s canonical=%s side=%s", src, p.ID, leg.Venue, canonical, leg.Side)
			return 0, false
		}
		netCarry += legCarry
		foundAny = true
	}
	if !foundAny {
		return 0, false
	}
	log.Printf("[autotrade] %s pos=%d: xasset netCarry=%.4f%% ann", src, p.ID, netCarry)
	return netCarry, true
}

// xassetLegCarryAnnual 单腿持有净收入（年化 %；正=收）。
// :perp → funding fact（BUY 付 −rate / SELL 收 +rate，按 SwapFactMaxAge 新鲜度闸；
// window>0 → 窗口内 facts 滚动均值年化，含周末零样本=诚实日均（D-027））；
// :mt4/:mt5 → swapRateFor（settled/spec/none，spec 源另过 xassetSpecFactFresh）×3.65 年化；
// window>0 时 spec 源同取窗内均值（settled 本就是 7d 窗口中位数）。
// 任一源缺/不新鲜/未知 venue 型 → ok=false（fail-closed）。
func (e *Engine) xassetLegCarryAnnual(ctx context.Context, venueKey, canonical, side string, maxAge, window time.Duration) (float64, bool) {
	bare := keydomain.BareOf(venueKey)
	switch {
	case keydomain.IsType(venueKey, "perp"):
		rate, ts, ok := e.fundingRateMeanForVenue(ctx, bare, canonical, window)
		if !ok {
			log.Printf("[autotrade] carry: funding fact missing venue=%s canonical=%s (fail-closed)", bare, canonical)
			return 0, false
		}
		if maxAge > 0 && time.Since(ts) > maxAge {
			log.Printf("[autotrade] carry: funding fact stale venue=%s canonical=%s >%v (fail-closed)", bare, canonical, maxAge)
			return 0, false
		}
		// 持有净收入：BUY（多头）付 funding=−rate；SELL（空头）收 funding=+rate。
		if side == "BUY" {
			return -rate, true
		}
		return rate, true
	case keydomain.IsType(venueKey, "mt5"), keydomain.IsType(venueKey, "mt4"):
		bps, src := e.swapRateFor(ctx, bare, canonical, side, window)
		if src == swapSrcNone {
			log.Printf("[autotrade] carry: swapRateFor none venue=%s canonical=%s side=%s (fail-closed)", bare, canonical, side)
			return 0, false
		}
		if src == swapSrcSpec && !e.xassetSpecFactFresh(ctx, bare, canonical, side) {
			return 0, false
		}
		return bps * 3.65, true
	case keydomain.IsType(venueKey, "spot"):
		return 0, true // D-014：现货腿无 funding/swap——carry=0 是事实非缺数据（FUNDING_HEDGE 正构型）
	default:
		log.Printf("[autotrade] carry: unknown venue type %s (fail-closed)", venueKey)
		return 0, false
	}
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

// fundingRateMeanForVenue D-027：(bareVenue, canonical) funding 滚动均值年化 %——
// window>0 取窗内 facts 均值（含周末零样本=诚实日均），ts=窗内最新样本时戳（供新鲜度闸）；
// window≤0 退化为 fundingRateForVenue 瞬时值（展示/灾难带口径）。
// 窗内零样本 → ok=false（fail-closed）。
func (e *Engine) fundingRateMeanForVenue(ctx context.Context, bareVenue, canonical string, window time.Duration) (rate float64, ts time.Time, ok bool) {
	if window <= 0 {
		return e.fundingRateForVenue(ctx, bareVenue, canonical)
	}
	return fact.FundingMeanAnnual(ctx, e.store, bareVenue, canonical, window) // D-034：共享单实现
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
			bare := keydomain.BareOf(legs[i].Venue)
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
