// reconcile_perp_d161.go —— D-161：perp 腿对账逐结算点口径。
// 病灶：SumCashflowsForDay 当日记账行=首轮 ledger 瞬时率×全日名义——率塌缩日点估计
// vs 实收结构性误报（#510 实证 1.6343 vs 0.7547=53.8%）。修法：结算点列
// {dayStart+k·ivl}（含首界，与 income ts 对齐——生产实证 okx 收入 ts=结算点+秒级
// 延迟），每点生效率=facts 末条 ts≤S、窗长=ivl 定值，粒度统一 (venue,symbol,day)：
// 期望=同 venue+symbol 全部 OPEN 仓窗级求和，实际=income_events 同粒度日和。
// 降级链（组级原子——income 日和账户级共享，混算=错配）：interval 缺/无名义源/
// 界空/任一腿 canonical·notional 不可解 → 全组回退日频旧径（fail-closed）；
// funding facts 零覆盖 → skip。禁区：income_events=实收唯一真值不回写、D-349 未过窗
// skip、D-437 heldFrac（结算点≤开仓不收）、阈值不抬。
package swapledger

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"arb/internal/data/fact"
	"arb/internal/data/keydomain"
	"arb/internal/store/pgstore"
)

// SetNotional 注入名义额解析（USDPerLotForLeg 实现；nil=全量回退日频旧径）。
func (r *SwapReconciler) SetNotional(n NotionalResolver) { r.notional = n }

// perpGroupKey 对账粒度键（venue 裸名 + 腿原生 symbol——income_events 同口径）。
type perpGroupKey struct{ venue, symbol string }

// perpGroupLeg 组内一条 OPEN 腿（带所属仓位供 OpenedAt 折窗与告警列名）。
type perpGroupLeg struct {
	pos pgstore.Position
	leg pgstore.PositionLeg
}

// settleBoundsOf 结算点列 {dayStart+k·ivl}（含首界 dayStart——与 income ts 对齐：
// income 落当日日和的结算点恰为 [dayStart,dayEnd) 内各界；生产实证 okx 收入 ts=
// 结算点+秒级延迟）。dayEnd 非整除 ivl → nil 回退（窗界不齐整不猜）。
func settleBoundsOf(dayStart, dayEnd time.Time, ivlSec int64) []time.Time {
	if ivlSec <= 0 || int64(dayEnd.Sub(dayStart)/time.Second)%ivlSec != 0 {
		return nil
	}
	ivl := time.Duration(ivlSec) * time.Second
	var out []time.Time
	for e := dayStart; e.Before(dayEnd); e = e.Add(ivl) {
		out = append(out, e)
	}
	return out
}

// rateAt funding 时序（ts 升序）结算点前值=末条 ts<=t 值；无 → false。
func rateAt(facts []fact.Fact, t time.Time) (float64, bool) {
	rate, ok := 0.0, false
	for _, f := range facts {
		if f.Ts.After(t) {
			break
		}
		rate, ok = f.Value, true
	}
	return rate, ok
}

// legWindowExpected 单腿逐窗期望和：Σ 结算点率 × notional × ivl/365（sign 与
// legSwapAmount 资金费符号同规）。返回 (期望值, 有率点数, 总点数)。
func legWindowExpected(facts []fact.Fact, bounds []time.Time, ivl time.Duration,
	openedAt *time.Time, notional float64, side string) (float64, int, int) {
	sign := 1.0
	if side == "BUY" {
		sign = -1.0
	}
	var exp float64
	covered := 0
	// 每结算点 S（窗长 ivl 定值）：率=facts 末条 ts≤S（#510 实证 00:00 结 0.607=
	// 率 16.16%(23:59 末条)×名义×8h/8760）。
	ivlFrac := ivl.Seconds() / 86400
	for _, e := range bounds {
		rate, ok := rateAt(facts, e)
		if !ok {
			continue
		}
		covered++
		if openedAt != nil && !e.After(*openedAt) {
			continue // 结算点≤开仓时刻——该窗持仓不产收
		}
		exp += sign * decimalAmount(rate, 100, DaysPerYear, notional, ivlFrac)
	}
	return exp, covered, len(bounds)
}

// legCanonical D-330 同款腿 canonical 解析（BUY=oppID 段、SELL=侧向三段链）——
// facts 按 canonical 键存（legSwapAmount 同源口径，跨 canonical 对不借用）。
func (r *SwapReconciler) legCanonical(ctx context.Context, pos pgstore.Position, leg pgstore.PositionLeg, legs []pgstore.PositionLeg) (string, error) {
	canonical := canonicalFromOppID(pos.OpportunityID)
	if leg.Side == "BUY" || canonical == "" {
		return canonical, nil
	}
	if opp, err := r.store.GetOpportunity(ctx, pos.OpportunityID); err == nil &&
		opp != nil && opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		return *opp.CanonicalSell, nil
	}
	maps, err := r.store.ListSymbolMappings(ctx, venueFromKey(leg.Venue))
	if err != nil {
		return "", fmt.Errorf("list symbol mappings: %w", err)
	}
	for _, m := range maps {
		if m.BrokerSymbol == leg.Symbol && m.Canonical != "" {
			return m.Canonical, nil
		}
	}
	return canonical, nil
}

// reconcilePerpGroup (venue,symbol,day) 粒度对账：期望=组内全腿逐窗求和，
// 实际=income 日和；任一腿不可窗算/部分窗无率 → 全组回退旧径逐腿比对；
// funding facts 零覆盖 → skip。返回告警数。
func (r *SwapReconciler) reconcilePerpGroup(ctx context.Context, key perpGroupKey,
	members []perpGroupLeg, day, now time.Time) (int, error) {
	dayStart := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.AddDate(0, 0, 1)

	var ivlSec int64
	var legsCache map[int64][]pgstore.PositionLeg
	type legCalc struct {
		m         perpGroupLeg
		canonical string
		series    []fact.Fact
		notional  float64
	}
	var calcs []legCalc
	windowable := r.notional != nil
	degrade := func() (int, error) {
		alerts := 0
		for _, m := range members {
			alerted, err := r.reconcileLegWithStore(ctx, m.pos, m.leg.Venue, m.leg.Symbol, day, now)
			if err != nil {
				log.Printf("[swap-reconcile] leg posID=%d venue=%s fallback: %v", m.pos.ID, m.leg.Venue, err)
				continue
			}
			if alerted {
				alerts++
			}
		}
		return alerts, nil
	}

	for _, m := range members {
		if !windowable {
			break
		}
		legs, ok := legsCache[m.pos.ID]
		if !ok {
			var err error
			if legs, err = r.store.ListPositionLegs(ctx, m.pos.ID); err != nil {
				return 0, fmt.Errorf("list legs %d: %w", m.pos.ID, err)
			}
			if legsCache == nil {
				legsCache = map[int64][]pgstore.PositionLeg{}
			}
			legsCache[m.pos.ID] = legs
		}
		canonical, err := r.legCanonical(ctx, m.pos, m.leg, legs)
		if err != nil || canonical == "" {
			log.Printf("[swap-reconcile] group %s/%s leg%d canonical 解失败→组回退: %v", key.venue, key.symbol, m.leg.LegNo, err)
			windowable = false
			continue
		}
		if ivlSec == 0 {
			ivlFacts, err := r.store.FactsSince(ctx, "funding_interval", key.venue, canonical, dayStart.Add(-2*24*time.Hour))
			if err != nil {
				return 0, fmt.Errorf("funding_interval facts: %w", err)
			}
			for _, f := range ivlFacts {
				if f.Ts.Before(dayEnd) {
					ivlSec = int64(math.Round(f.Value))
				}
			}
		}
		series, err := r.store.FactsSince(ctx, "funding", key.venue, canonical, dayStart.Add(-36*time.Hour))
		if err != nil {
			return 0, fmt.Errorf("funding facts since: %w", err)
		}
		side := "ask"
		if m.leg.Side == "SELL" {
			side = "bid"
		}
		usdPerLot, ok := r.notional.USDPerLotForLeg(canonical, m.leg.Venue, side)
		if !ok || usdPerLot <= 0 {
			log.Printf("[swap-reconcile] group %s/%s leg%d notional 缺→组回退", key.venue, key.symbol, m.leg.LegNo)
			windowable = false
			continue
		}
		calcs = append(calcs, legCalc{m, canonical, series, usdPerLot * m.leg.EffectiveQty()})
	}
	bounds := settleBoundsOf(dayStart, dayEnd, ivlSec)
	if !windowable || ivlSec <= 0 || len(bounds) == 0 {
		log.Printf("[swap-reconcile] group %s/%s %s 窗径不可用（notional=%v ivl=%d bounds=%d）→回退日频旧径",
			key.venue, key.symbol, day.Format("2006-01-02"), r.notional != nil, ivlSec, len(bounds))
		return degrade()
	}

	expect := 0.0
	coveredTotal, uncoveredTotal := 0, 0
	for _, c := range calcs {
		exp, covered, total := legWindowExpected(c.series, bounds, time.Duration(ivlSec)*time.Second,
			c.m.pos.OpenedAt, c.notional, c.m.leg.Side)
		expect += exp
		coveredTotal += covered
		uncoveredTotal += total - covered
	}
	if coveredTotal == 0 {
		// funding facts 零覆盖 → skip（率史缺席无法举证期望，不猜）
		log.Printf("[swap-reconcile] group %s/%s %s funding facts 零覆盖 skip", key.venue, key.symbol, day.Format("2006-01-02"))
		return 0, nil
	}
	if uncoveredTotal > 0 {
		log.Printf("[swap-reconcile] group %s/%s %s facts 窗覆盖不足（%d 窗无率）→回退日频旧径",
			key.venue, key.symbol, day.Format("2006-01-02"), uncoveredTotal)
		return degrade()
	}

	actualUSD, rows, err := r.store.SumIncomeForDay(ctx, key.venue, key.symbol, day)
	if err != nil {
		return 0, err
	}
	// 0 行且期望>0 且窗未过 → skip（0 行≠实收 0）。
	if rows == 0 && expect > 0 && !cexSettleWindowPassed(day, now) {
		return 0, nil
	}
	deviation := math.Abs(expect - actualUSD)
	denom := math.Max(math.Max(math.Abs(expect), math.Abs(actualUSD)), 0.01)
	deviationPct := deviation / denom * 100
	level := alertLevel(deviationPct, r.deviationPct)
	if level == "" {
		return 0, nil
	}
	var ids []string
	for _, m := range members {
		ids = append(ids, fmt.Sprintf("#%d", m.pos.ID))
	}
	msg := fmt.Sprintf("[息差对账] 持仓 %s（%s %s）：公式值 $%.4f 实际收入 $%.4f，偏差 %.1f%%（阈值 %.1f%%，逐窗口径）",
		strings.Join(ids, ","), key.venue, day.Format("2006-01-02"), expect, actualUSD, deviationPct, r.deviationPct)
	if err := r.store.InsertAlert(ctx, nil, level, msg); err != nil {
		return 0, fmt.Errorf("insert alert: %w", err)
	}
	log.Printf("[swap-reconcile] ALERT %s: %s", level, msg)
	return 1, nil
}

// collectLegs 逐腿分流：perp 腿入跨仓分组（D-161），MT 腿照旧比对。
func (r *SwapReconciler) collectLegs(ctx context.Context, pos pgstore.Position, day time.Time, perpGroups map[perpGroupKey][]perpGroupLeg) (int, error) {
	legs, err := r.store.ListPositionLegs(ctx, pos.ID)
	if err != nil {
		return 0, fmt.Errorf("list legs: %w", err)
	}

	alerts := 0
	for _, leg := range legs {
		if leg.Phase != "OPEN" {
			continue
		}
		// per-leg 预检查：该腿比对日未记 → 跳过该腿（swapledger 未记，不误告警）
		// kind per-leg 判定（XASSET_CARRY perp 腿=funding、MT 腿=swap）。
		// perp 腿比对日=昨日（income 多窗结算需完整日对平，日中部分窗对全日公式必误报）；
		// MT 腿=当日不变（快照差天然含时长）。昨日无 cashflow（今日新开仓）→ 自然 skip 零特例。
		cmpDay := day
		isPerp := keydomain.IsType(leg.Venue, "perp")
		if isPerp {
			cmpDay = day.AddDate(0, 0, -1)
		}
		kind := cashflowKind(pos.Type, leg.Venue)
		has, err := r.store.HasCashflowForLegDay(ctx, pos.ID, leg.LegNo, kind, cmpDay)
		if err != nil {
			return 0, fmt.Errorf("check cashflow: %w", err)
		}
		if !has {
			continue
		}
		if isPerp {
			perpGroups[perpGroupKey{venueFromKey(leg.Venue), leg.Symbol}] = append(
				perpGroups[perpGroupKey{venueFromKey(leg.Venue), leg.Symbol}], perpGroupLeg{pos, leg})
			continue
		}
		alerted, err := r.reconcileLegWithStore(ctx, pos, leg.Venue, leg.Symbol, cmpDay, day)
		if err != nil {
			log.Printf("[swap-reconcile] leg posID=%d venue=%s: %v", pos.ID, leg.Venue, err)
			continue
		}
		if alerted {
			alerts++
		}
	}
	return alerts, nil
}
