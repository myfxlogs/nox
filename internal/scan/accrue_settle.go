// accrue_settle.go D-474a S1：结算对齐累积——「快照费率×墙钟外推」改「已结算事件边界累积」。
// 双腿行取 min（共同前缀已结算窗）防单腿时钟超前造成重复记/漏记；边界事实缺 → fail-closed
// 本轮不 accrue（禁回退快照外推，D-079 禁条）。
package scan

import (
	"context"
	"fmt"
	"math"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// PaperFactSource 结算对齐累积的 facts/映射/tick 窄读面（*pgstore.Store 原生实现）。
type PaperFactSource interface {
	LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error)
	FactsSince(ctx context.Context, kind, venue, symbol string, since time.Time) ([]fact.Fact, error)
	ListSymbolMappings(ctx context.Context, venue string) ([]pgstore.SymbolMapping, error)
	TicksWindow(ctx context.Context, venue, symbol string, since time.Time) ([]pgstore.TickRow, error)
}

// PaperLegAccrue 单腿结算累积输入。
// Kind=fact.KindFunding（CEX perp 腿）或 fact.KindSwapLong/KindSwapShort（MT 腿）；
// Symbol=facts 键（broker 裸 symbol——collectMtSwaps 按 BrokerSymbol 匹配实证）。
// SideFactor：funding 腿方向（+1 收/−1 付）；swap 腿恒 1（fact 值已含方向：正=收负=付）。
// CurRate/CurOK：funding 腿当期年化率（%/yr，universe/radar 行值同源）——历史缺界的
// 当期回落源（SSOT 列名源）；swap 腿不用。
type PaperLegAccrue struct {
	Kind       string
	Venue      string
	Symbol     string
	SideFactor float64
	CurRate    float64
	CurOK      bool
}

// legBoundary 单边界记账项：b=结算时刻，amt=该期入账（USD，符号含方向）。
type legBoundary struct {
	b   time.Time
	amt float64
}

// fundingHistTol funding_history 行 ts 与结算边界的对齐容差（采集 ts=结算时刻，±2min 宽松带）。
const fundingHistTol = 2 * time.Minute

// fundingLegBoundaries CEX funding 腿 (base, now] 已结算边界逐期金额。
// 边界序列=facts funding_next_settle 反推（ns−k·iv）；每期实率=funding_history
// （ts≈b）优先，缺界回落当期 funding 率（CurRate，venue 当期值——同 venue 原生数据，
// 非跨 venue 借用）。ns/iv fact 缺或 CurRate 与历史双缺 → ok=false。
func fundingLegBoundaries(ctx context.Context, st PaperFactSource, leg PaperLegAccrue,
	base, now time.Time, qty float64) ([]legBoundary, time.Time, bool, error) {
	if leg.Symbol == "" {
		return nil, time.Time{}, false, nil // 空 symbol 会退成全 venue 通配查询 → fail-closed
	}
	nsF, err := st.LatestFacts(ctx, fact.KindFundingNextSettle, leg.Venue, leg.Symbol)
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("next_settle %s|%s: %w", leg.Venue, leg.Symbol, err)
	}
	ivF, err := st.LatestFacts(ctx, fact.KindFundingInterval, leg.Venue, leg.Symbol)
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("interval %s|%s: %w", leg.Venue, leg.Symbol, err)
	}
	if len(nsF) == 0 || len(ivF) == 0 || ivF[0].Value <= 0 {
		return nil, time.Time{}, false, nil // 边界事实缺 → fail-closed
	}
	ns := time.Unix(int64(nsF[0].Value), 0)
	iv := time.Duration(int64(ivF[0].Value)) * time.Second
	// 边界枚举：ns, ns−iv, … 取 ≤now 且 >base（ns≤now=陈旧 next_settle 仍枚举真实边界）。
	var bounds []time.Time
	for b := ns; b.After(base); b = b.Add(-iv) {
		if !b.After(now) {
			bounds = append(bounds, b)
		}
		if len(bounds) > 2000 { // 防御：异常 interval 防爆内存（正常 ≤7d/1h=168）
			break
		}
	}
	// last ≤now 边界（结算水位，无新界也须取——min 共同前缀基准）。
	water := base
	if all := lastBoundaryLE(ns, iv, now); !all.IsZero() {
		water = all
	}
	if len(bounds) == 0 {
		return nil, water, true, nil
	}
	hist, err := st.FactsSince(ctx, fact.KindFundingHistory, leg.Venue, leg.Symbol, base.Add(-fundingHistTol))
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("funding_history %s|%s: %w", leg.Venue, leg.Symbol, err)
	}
	histByTs := make(map[int64]float64, len(hist))
	for _, h := range hist {
		histByTs[h.Ts.Unix()] = h.Value
	}
	rateAt := func(b time.Time) (float64, bool) {
		for ts, v := range histByTs {
			if math.Abs(float64(ts-b.Unix())) <= fundingHistTol.Seconds() {
				return v, true
			}
		}
		return leg.CurRate, leg.CurOK // 历史缺界回落当期率（SSOT 源），双缺→false
	}
	ppy := float64(time.Hour*24*365) / float64(iv) // periods_per_year
	out := make([]legBoundary, 0, len(bounds))
	for i := len(bounds) - 1; i >= 0; i-- { // bounds 降序→升序记账
		r, ok := rateAt(bounds[i])
		if !ok {
			return nil, time.Time{}, false, nil // 该期实率不可知 → fail-closed
		}
		out = append(out, legBoundary{b: bounds[i], amt: leg.SideFactor * r / 100 * qty / ppy})
	}
	return out, water, true, nil
}

// lastBoundaryLE 返回 ≤now 的最后结算边界（无→zero）。
func lastBoundaryLE(ns time.Time, iv time.Duration, now time.Time) time.Time {
	// D-028 ①：okx funding_next_settle 领先可达两个结算窗以上（00:00 结算后帧报 16:00）——
	// 单次回退不够 → water=base → 零边界入窗 accrue 恒 0 且 ok=true 静默。循环回退至 ≤now，
	// 有界上限=8h 网格×8 年（防异常 iv 爆循环；正常 1h 网格同理覆盖）。
	if iv <= 0 {
		return time.Time{}
	}
	for i := 0; ns.After(now) && i < 366*8; i++ {
		ns = ns.Add(-iv)
	}
	if ns.After(now) {
		return time.Time{}
	}
	return ns
}

// mtSettleHour MT swap 日结边界：UTC 00:00（与 swap_settled_profile「结算日 UTC 00:00」
// 同约定，D-180 先例）；triple_swap_day fact 命中该日 weekday → 当日 ×3。
const mtTripleMult = 3.0

// swapLegBoundaries MT swap 腿 (base, now] 已结算边界（UTC 日界）逐期金额。
// 日率序列=FactsSince(swap kind) 窗内值：每日取「最新 ts≤边界」值，窗内首界前无值→
// 用最早已知值（同日率 venue 原生，不跨源）；窗内零行 → 回落 LatestFacts 当期值
// （swap 为经纪商静态日率配置，单值口径=该腿 venue 原生）。双缺 → ok=false。
// swap_settled_long/short（usd_lot_day 实测结算画像）存在且 lots 可换算
// （mt_symbol_meta cs × 最新 tick mid 价）→ 优先实收路径。
func swapLegBoundaries(ctx context.Context, st PaperFactSource, leg PaperLegAccrue,
	base, now time.Time, qty float64) ([]legBoundary, time.Time, bool, error) {
	if leg.Symbol == "" || leg.Venue == "" {
		return nil, time.Time{}, false, nil // 空键会退成通配查询 → fail-closed
	}
	// 日界枚举：UTC 午夜（D-022：日历日取 UTC——原 base/now 的本地时区 Y/M/D 拼 UTC 午夜，
	// 本地日期跨日后（如 UTC 22:00=+0800 次日）water 落 now 之后被双分支弃取→water=base→
	// 零边界入窗 accrue 恒 0；06:50 UTC 本地同日则绿=时相 flake 根因）。
	utcBase, utcNow := base.UTC(), now.UTC()
	first := time.Date(utcBase.Year(), utcBase.Month(), utcBase.Day(), 0, 0, 0, 0, time.UTC)
	if !first.After(utcBase) {
		first = first.AddDate(0, 0, 1)
	}
	var bounds []time.Time
	for d := first; !d.After(utcNow); d = d.AddDate(0, 0, 1) {
		bounds = append(bounds, d)
	}
	water := base
	if lw := time.Date(utcNow.Year(), utcNow.Month(), utcNow.Day(), 0, 0, 0, 0, time.UTC); !lw.After(now) && lw.After(base) {
		water = lw
	} else if !lw.After(now) {
		water = lw // ≤base 的今日界也是水位（min 前缀基准用）
	}
	// 窗内率序列
	hist, err := st.FactsSince(ctx, leg.Kind, leg.Venue, leg.Symbol, base.Add(-30*24*time.Hour))
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("swap hist %s|%s|%s: %w", leg.Kind, leg.Venue, leg.Symbol, err)
	}
	latest, err := st.LatestFacts(ctx, leg.Kind, leg.Venue, leg.Symbol)
	if err != nil {
		return nil, time.Time{}, false, fmt.Errorf("swap latest %s|%s|%s: %w", leg.Kind, leg.Venue, leg.Symbol, err)
	}
	if len(hist) == 0 && len(latest) == 0 {
		return nil, time.Time{}, false, nil // 率源双缺 → fail-closed
	}
	rateAt := func(b time.Time) float64 { // bps_daily
		v, ok := latestLE(hist, b)
		if ok {
			return v
		}
		if len(hist) > 0 {
			return hist[0].Value // 窗内最早已知值（率在该界前已在生效的诚实近似）
		}
		return latest[0].Value
	}
	// triple_swap_day fact（周几=值域 time.Weekday）
	tripleDay := -1
	if tf, err := st.LatestFacts(ctx, fact.KindTripleSwapDay, leg.Venue, leg.Symbol); err == nil && len(tf) > 0 {
		tripleDay = int(tf[0].Value)
	}
	mult := func(d time.Time) float64 {
		if int(d.Weekday()) == tripleDay {
			return mtTripleMult
		}
		return 1
	}
	// swap_settled 实收优先（usd_lot_day×lots；lots=qty/(cs×price)，换算输入缺→回落率路径）
	settledPerDay, sok := mtSettledPerDayUSD(ctx, st, leg, qty, now)
	out := make([]legBoundary, 0, len(bounds))
	for _, b := range bounds {
		if sok {
			out = append(out, legBoundary{b: b, amt: settledPerDay * mult(b)})
		} else {
			out = append(out, legBoundary{b: b, amt: rateAt(b) / 1e4 * qty * mult(b)})
		}
	}
	return out, water, true, nil
}

// latestLE 升序序列中 ts≤b 的最后值。
func latestLE(hist []fact.Fact, b time.Time) (float64, bool) {
	v, ok := 0.0, false
	for _, h := range hist {
		if h.Ts.After(b) {
			break
		}
		v, ok = h.Value, true
	}
	return v, ok
}

// mtSettledPerDayUSD swap_settled 实收换算：usd_lot_day × lots（lots=qty/(cs×mid)）。
// now 注入时钟（D-022：tick 窗下限随调用轮 now，禁隐式墙钟）。
// 换算输入（settled fact / contract_size / 最新 tick）任一缺 → ok=false 回落率路径。
func mtSettledPerDayUSD(ctx context.Context, st PaperFactSource, leg PaperLegAccrue, qty float64, now time.Time) (float64, bool) {
	settleKind := fact.KindSwapSettledLong
	if leg.Kind == fact.KindSwapShort {
		settleKind = fact.KindSwapSettledShort
	}
	sf, err := st.LatestFacts(ctx, settleKind, leg.Venue, leg.Symbol)
	if err != nil || len(sf) == 0 {
		return 0, false
	}
	raw := leg.Symbol // 调用方已解析为 broker 裸符号（tick/meta 同键）
	meta, err := st.LatestFacts(ctx, fact.KindMTSymbolMeta, leg.Venue, raw)
	if err != nil || len(meta) == 0 || meta[0].Value <= 0 {
		return 0, false
	}
	ticks, err := st.TicksWindow(ctx, leg.Venue, raw, now.Add(-paperMtmTickMaxAge))
	if err != nil || len(ticks) == 0 {
		return 0, false
	}
	t := ticks[len(ticks)-1]
	mid := (t.Bid + t.Ask) / 2
	if mid <= 0 {
		return 0, false
	}
	return sf[0].Value * qty / (meta[0].Value * mid), true
}

// AccruePaperLegs 双腿（或单腿）结算对齐累积：各腿独立枚举 (base, now] 边界，
// 推进水位=min（各腿 last≤now 界）——共同已结算前缀内逐期实率求和。
// 返回（总 add, settledTo, ok）；任一腿 fail-closed（边界/率源缺）→ ok=false。
func AccruePaperLegs(ctx context.Context, st PaperFactSource, legs []PaperLegAccrue,
	base, now time.Time, qty float64) (float64, time.Time, bool, error) {
	if !now.After(base) {
		return 0, base, true, nil
	}
	type legRes struct {
		bounds []legBoundary
		water  time.Time
	}
	res := make([]legRes, len(legs))
	settledTo := now
	for i, leg := range legs {
		var bounds []legBoundary
		var water time.Time
		var ok bool
		var err error
		switch leg.Kind {
		case fact.KindFunding:
			bounds, water, ok, err = fundingLegBoundaries(ctx, st, leg, base, now, qty)
		case fact.KindSwapLong, fact.KindSwapShort:
			bounds, water, ok, err = swapLegBoundaries(ctx, st, leg, base, now, qty)
		default:
			return 0, time.Time{}, false, fmt.Errorf("unknown leg kind %s", leg.Kind)
		}
		if err != nil || !ok {
			return 0, time.Time{}, ok, err
		}
		res[i] = legRes{bounds: bounds, water: water}
		if water.Before(settledTo) {
			settledTo = water
		}
	}
	if settledTo.Before(base) {
		settledTo = base // 水位只前进不回退
	}
	add := 0.0
	for _, r := range res {
		for _, b := range r.bounds {
			if !b.b.After(settledTo) {
				add += b.amt
			}
		}
	}
	return add, settledTo, true, nil
}
