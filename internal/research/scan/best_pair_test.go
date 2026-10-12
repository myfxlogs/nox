package scan

// T1-T3：mt×mt 配对纯函数 + 纸面步进（开仓/累积/退出/双 NULL/cap/streak）。
import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"arb/internal/data/fact"
	"arb/internal/store/pgstore"
)

func mkSwap(canon, venue, sym string, val float64) mtSwap {
	return mtSwap{venue: venue, canonical: canon, symbol: sym, val: val, ts: time.Now()}
}

// TestD465_BestPair T1：三 venue 两 canonical 构造 swap 矩阵——最优 (A,B,structure)
// 选取 + 自配排除 + 并列确定性。
func TestD465_BestPair(t *testing.T) {
	longBook := map[string]mtSwap{
		"XAU|A": mkSwap("XAU", "A", "XAUUSD.a", -3),
		"XAU|B": mkSwap("XAU", "B", "XAUUSD.b", -8),
		"XAU|C": mkSwap("XAU", "C", "XAUUSD.c", -2),
		"WTI|A": mkSwap("WTI", "A", "OIL.a", -4),
	}
	shortBook := map[string]mtSwap{
		"XAU|A": mkSwap("XAU", "A", "XAUUSD.a", 1),
		"XAU|B": mkSwap("XAU", "B", "XAUUSD.b", 9),
		"XAU|C": mkSwap("XAU", "C", "XAUUSD.c", 4),
		"WTI|B": mkSwap("WTI", "B", "OIL.b", 2),
	}
	// XAU 最优：short_a_long_b B×C net=9+(−2)=7（long_a_short_b C×B=−2+9=7 并列，
	// 字典序 "B|C" < "C|B" → 短 B 长 C 胜）。
	p, ok := bestMtmPair("XAU", longBook, shortBook)
	if !ok || p.venueA != "B" || p.venueB != "C" || p.side != "short_a_long_b" || p.net != 7 {
		t.Fatalf("best=%+v ok=%v want B×C short_a_long_b net=7", p, ok)
	}
	// WTI：仅 A 有 long、B 有 short → 唯一对 A×B long_a_short_b net=−2。
	p2, ok := bestMtmPair("WTI", longBook, shortBook)
	if !ok || p2.venue() != "A×B" || p2.side != "long_a_short_b" || p2.net != -2 {
		t.Fatalf("wti best=%+v ok=%v", p2, ok)
	}
	// 自配排除：单 venue 双侧簿齐 → 无配对。
	lb := map[string]mtSwap{"XAU|A": mkSwap("XAU", "A", "s", -3)}
	sb := map[string]mtSwap{"XAU|A": mkSwap("XAU", "A", "s", 9)}
	if _, ok := bestMtmPair("XAU", lb, sb); ok {
		t.Fatal("single-venue self-pair must be excluded")
	}
	// 长腿缺簿侧 venue 不参与配对：longBook 只 A、shortBook A+C → 唯一 A×C。
	lb2 := map[string]mtSwap{"XAU|A": mkSwap("XAU", "A", "s", -1)}
	sb2 := map[string]mtSwap{"XAU|A": mkSwap("XAU", "A", "s", 0), "XAU|C": mkSwap("XAU", "C", "s", 6)}
	p3, ok := bestMtmPair("XAU", lb2, sb2)
	if !ok || p3.venue() != "A×C" || p3.net != 5 {
		t.Fatalf("best=%+v ok=%v", p3, ok)
	}
}

// fakeMtmPaperStore PaperStore 最小实现（mt×mt 轨用到的方法集）。
type fakeMtmPaperStore struct {
	rows     map[int64]*pgstore.PaperSpotCarry
	nextID   int64
	ticks    map[string][]pgstore.TickRow       // venue|symbol → ticks
	universe map[string]pgstore.CexUniverseRow  // D-463：spot 步进候选源
	facts    map[string][]fact.Fact             // D-474a：kind|venue|symbol → 升序 facts
	equiv    []pgstore.AssetEquivEntry          // D-043：class 键行腿解析测试面
	mappings map[string][]pgstore.SymbolMapping // venue→映射（D-043 class 行 raw 解析）
	accrues  []struct {
		id        int64
		last, add float64
		streak    int
	}
	exits []struct {
		id     int64
		reason string
	}
	// 注入钟面——EntryTs/LastAccrueTs 与步进轮 now 同钟；零值回落墙钟
	// （真实钟测试不变）。混双钟曾使 base=真实 now 越过注入轮，UTC 日界空窗
	// accrue 恒 0（D-465 T2 腐化根因）。
	now    time.Time
	alerts []string // D-144：InsertAlert 留痕（level|msg）
}

func newFakeMtmPaperStore() *fakeMtmPaperStore {
	return &fakeMtmPaperStore{rows: map[int64]*pgstore.PaperSpotCarry{}, ticks: map[string][]pgstore.TickRow{}}
}

func (f *fakeMtmPaperStore) ListCexUniverse(context.Context) (map[string]pgstore.CexUniverseRow, error) {
	return f.universe, nil
}

// D-051② 质量闸读面桩（mt×mt 轨测试不触闸——阈零值闸不激活）。
func (f *fakeMtmPaperStore) CexDepthAvgUSD(context.Context, string, string, time.Time) (float64, int, error) {
	return 0, 0, nil
}
func (f *fakeMtmPaperStore) FundingSettleAvgs(context.Context, time.Time) (map[string]map[string]pgstore.SettleAvg, error) {
	return nil, nil
}
func (f *fakeMtmPaperStore) ListActivePaperSpotCarry(context.Context) ([]pgstore.PaperSpotCarry, error) {
	var out []pgstore.PaperSpotCarry
	for _, r := range f.rows {
		if r.Status == "active" {
			out = append(out, *r)
		}
	}
	return out, nil
}
func (f *fakeMtmPaperStore) ListAccrablePaperSpotCarry(context.Context) ([]pgstore.PaperSpotCarry, error) {
	var out []pgstore.PaperSpotCarry
	for _, r := range f.rows {
		if r.Status == "active" || r.Status == "demo" {
			out = append(out, *r)
		}
	}
	return out, nil
}
func (f *fakeMtmPaperStore) InsertPaperSpotCarry(_ context.Context, r *pgstore.PaperSpotCarry) error {
	for _, x := range f.rows {
		if x.VenueA == r.VenueA && x.VenueB == r.VenueB && x.Symbol == r.Symbol && (x.Status == "active" || x.Status == "demo") {
			return pgstore.ErrPaperExists
		}
	}
	f.nextID++
	cp := *r
	cp.ID = f.nextID
	cp.Status = "active"
	cp.EntryTs = f.now
	if cp.EntryTs.IsZero() {
		cp.EntryTs = time.Now()
	}
	ts := cp.EntryTs
	cp.LastAccrueTs = &ts // 与真库 insert last_accrue_ts=now() 同义（非回迁哨兵）
	f.rows[cp.ID] = &cp
	return nil
}
func (f *fakeMtmPaperStore) AccruePaperSpotCarry(_ context.Context, id int64, last, add float64, streak, miss int, basis *float64, accrueTo *time.Time, replace bool) error {
	f.accrues = append(f.accrues, struct {
		id        int64
		last, add float64
		streak    int
	}{id, last, add, streak})
	if r, ok := f.rows[id]; ok {
		r.LowStreak = streak
		r.MissStreak = miss
		if replace {
			r.AccruedUSD = add
		} else {
			r.AccruedUSD += add
		}
		r.LastFundingAnn = &last
		r.LastBasisBps = basis
		if accrueTo != nil {
			ts := *accrueTo
			r.LastAccrueTs = &ts
		}
	}
	return nil
}

// demo 实收读面（fake 无 income 源→缺席窗 +0 语义）。
func (f *fakeMtmPaperStore) SumIncomeWindow(context.Context, string, string, time.Time, time.Time) (float64, int, error) {
	return 0, 0, nil
}
func (f *fakeMtmPaperStore) AnnotatePaperNote(_ context.Context, id int64, marker string) error {
	if r, ok := f.rows[id]; ok && !strings.Contains(r.Note, marker) {
		r.Note += " | " + marker
	}
	return nil
}

// facts 读面（测试注入 f.facts[kind|venue|symbol]）。
func (f *fakeMtmPaperStore) LatestFacts(_ context.Context, kind, venue, symbol string) ([]fact.Fact, error) {
	return f.facts[kind+"|"+venue+"|"+symbol], nil
}
func (f *fakeMtmPaperStore) FactsSince(_ context.Context, kind, venue, symbol string, since time.Time) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, x := range f.facts[kind+"|"+venue+"|"+symbol] {
		if !x.Ts.Before(since) {
			out = append(out, x)
		}
	}
	return out, nil
}
func (f *fakeMtmPaperStore) ListSymbolMappings(_ context.Context, venue string) ([]pgstore.SymbolMapping, error) {
	return f.mappings[venue], nil
}
func (f *fakeMtmPaperStore) ListAssetEquiv(_ context.Context) ([]pgstore.AssetEquivEntry, error) {
	return f.equiv, nil
}
func (f *fakeMtmPaperStore) ExitPaperSpotCarry(_ context.Context, id int64, reason string, _ *float64) error {
	f.exits = append(f.exits, struct {
		id     int64
		reason string
	}{id, reason})
	if r, ok := f.rows[id]; ok { // 冻结：退出行不再进 accrable
		r.Status = "exited"
		r.ExitReason = reason
	}
	return nil
}
func (f *fakeMtmPaperStore) TicksWindow(_ context.Context, venue, symbol string, since time.Time) ([]pgstore.TickRow, error) {
	var out []pgstore.TickRow
	for _, t := range f.ticks[venue+"|"+symbol] {
		if !t.Ts.Before(since) {
			out = append(out, t)
		}
	}
	return out, nil
}

// 其余 PaperStore 成员（mt×mt 轨不用，桩返回）。
func (f *fakeMtmPaperStore) XAssetMonitorRowNear(context.Context, string, string, string, time.Time, time.Duration) (float64, bool, error) {
	return 0, false, nil
}
func (f *fakeMtmPaperStore) LatestXAssetEntryGate(context.Context, string, string, string, time.Duration) (float64, float64, float64, bool, bool, error) {
	return 0, 0, 0, false, false, nil
}
func (f *fakeMtmPaperStore) HasXAssetMonitorRow(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (f *fakeMtmPaperStore) LoadXAssetAllowCombos(context.Context, string) ([]pgstore.XAssetAllowRow, error) {
	return nil, nil
}
func (f *fakeMtmPaperStore) LatestPaperExitTs(context.Context, string, string, string) (*time.Time, error) {
	return nil, nil
}
func (f *fakeMtmPaperStore) InsertAlert(_ context.Context, _ *int64, level, msg string) error {
	f.alerts = append(f.alerts, level+"|"+msg) // D-144：闸/哨兵断言面
	return nil
}

// mtmFixture 两 MT 商 swap facts（bps_daily；annualized=mean×365/100）。
// venueB swap_short +3bps/d → +10.95%/yr；venueA swap_long −1bps/d → −3.65%/yr。
func mtmFixture() *fakeScanStore {
	return &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "swap_long", Venue: "mtA", Symbol: "XAUUSD.a", Value: -1, Ts: time.Now()},
			{Kind: "swap_short", Venue: "mtA", Symbol: "XAUUSD.a", Value: 0.5, Ts: time.Now()},
			{Kind: "swap_long", Venue: "mtB", Symbol: "XAUUSD.b", Value: -2, Ts: time.Now()},
			{Kind: "swap_short", Venue: "mtB", Symbol: "XAUUSD.b", Value: 3, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "mtA", BrokerSymbol: "XAUUSD.a", Canonical: "XAU"},
			{Venue: "mtB", BrokerSymbol: "XAUUSD.b", Canonical: "XAU"},
		},
	}
}

// TestD465_StepOpenAccrueExit T2：开仓（3 轮 streak 后）→ 累积 → 低息两轮退出。
// 时钟注入——全轮挂固定 UTC 时段（06:50 与 22:00 UTC 各跑一次；22:00 UTC=
// +0800 本地次日，旧 swapLegBoundaries 本地日历日拼 UTC 午夜的破相点）。
// 基日钉周三（2026-10-07）——+24h 累积轮须落在周内（周末 venue 旗标会拦截
// 累积轮=「断言不依赖星期」的另一半；周五基日 +24h 落周六曾被 battery 抓到）。
func TestD465_StepOpenAccrueExit(t *testing.T) {
	for _, utc := range []string{"06:50", "22:00"} {
		t.Run("utc_"+utc, func(t *testing.T) {
			h, _ := time.Parse("15:04", utc)
			d465StepOpenAccrueExitAt(t, time.Date(2026, 10, 7, h.Hour(), h.Minute(), 0, 0, time.UTC))
		})
	}
}

func d465StepOpenAccrueExitAt(t *testing.T, now time.Time) {
	ctx := context.Background()
	sc := NewCarryUniverseScanner(mtmFixture())
	st := newFakeMtmPaperStore()
	st.now = now
	st.ticks["mtA|XAUUSD.a"] = []pgstore.TickRow{{Ts: now.Add(-time.Minute), Venue: "mtA", Symbol: "XAUUSD.a", Bid: 100, Ask: 100.05}}
	st.ticks["mtB|XAUUSD.b"] = []pgstore.TickRow{{Ts: now.Add(-time.Minute), Venue: "mtB", Symbol: "XAUUSD.b", Bid: 99.9, Ask: 99.95}}
	// 结算对齐累积需双腿 facts 面（bps_daily 日率，UTC 日界）。
	st.facts = map[string][]fact.Fact{
		"swap_long|mtA|XAUUSD.a":  {{Kind: "swap_long", Venue: "mtA", Symbol: "XAUUSD.a", Value: -1, Ts: now}},
		"swap_short|mtA|XAUUSD.a": {{Kind: "swap_short", Venue: "mtA", Symbol: "XAUUSD.a", Value: 0.5, Ts: now}},
		"swap_long|mtB|XAUUSD.b":  {{Kind: "swap_long", Venue: "mtB", Symbol: "XAUUSD.b", Value: -2, Ts: now}},
		"swap_short|mtB|XAUUSD.b": {{Kind: "swap_short", Venue: "mtB", Symbol: "XAUUSD.b", Value: 3, Ts: now}},
	}
	es := NewEntryStreak()
	// 前两轮 streak 未满不开。
	for i := 0; i < PaperEntryStreakRounds-1; i++ {
		opened, _, err := sc.StepPaperMtmCarry(ctx, st, es, now, nil)
		if err != nil || len(opened) != 0 {
			t.Fatalf("round %d opened=%v err=%v want none", i, opened, err)
		}
	}
	opened, _, err := sc.StepPaperMtmCarry(ctx, st, es, now, nil)
	if err != nil || len(opened) != 1 {
		t.Fatalf("open: opened=%v err=%v", opened, err)
	}
	row := st.rows[1]
	// 最优对：long_a_short_b A×B net=−3.65+10.95=7.3。
	if row.VenuePair() != "mtA×mtB" || row.Symbol != "XAU" || row.Structure != "cfd_cfd" || row.Side != "long_a_short_b" {
		t.Fatalf("row=%+v", row)
	}
	if math.Abs(row.EntryFundingAnn-7.3) > 0.01 {
		t.Fatalf("net=%v want≈7.3", row.EntryFundingAnn)
	}
	if row.MtVenue == nil || *row.MtVenue != "mtB" {
		t.Fatalf("MtVenue=%v want mtB（short 腿）", row.MtVenue)
	}
	if row.EntryBasisBps == nil || row.EstRoundTripFeeUSD == nil {
		t.Fatal("tick 齐时 basis/fee 不得 NULL")
	}
	// 累积：下一轮（1 天后）net 仍 7.3 → accrued>0、streak=0。
	later := now.Add(24 * time.Hour)
	_, exited, err := sc.StepPaperMtmCarry(ctx, st, es, later, nil)
	if err != nil || exited != 0 {
		t.Fatalf("accrue round exited=%d err=%v", exited, err)
	}
	if len(st.accrues) != 1 || st.accrues[0].add <= 0 || st.accrues[0].streak != 0 {
		t.Fatalf("accrues=%+v", st.accrues)
	}
	// 退出（D-474a S3）：swap 利差翻负 → net≈−2.92<0 当轮退 reason=adverse
	// （不再等 low_streak 轮次）。net=(−1+0.2)bps/d×3.65=−2.92%/yr。
	sc2 := NewCarryUniverseScanner(&fakeScanStore{
		facts: []fact.Fact{
			{Kind: "swap_long", Venue: "mtA", Symbol: "XAUUSD.a", Value: -1, Ts: now},
			{Kind: "swap_short", Venue: "mtA", Symbol: "XAUUSD.a", Value: 0.5, Ts: now},
			{Kind: "swap_long", Venue: "mtB", Symbol: "XAUUSD.b", Value: -0.1, Ts: now},
			{Kind: "swap_short", Venue: "mtB", Symbol: "XAUUSD.b", Value: 0.2, Ts: now},
		},
		mappings: mtmFixture().mappings,
	})
	_, exited, err = sc2.StepPaperMtmCarry(ctx, st, NewEntryStreak(), later, nil)
	if err != nil {
		t.Fatal(err)
	}
	if exited != 1 || len(st.exits) != 1 || st.exits[0].reason != "adverse" {
		t.Fatalf("exits=%+v want adverse（net<0 当轮退）", st.exits)
	}
}

// TestD465_StepNullAndCap T2b：tick 缺→basis/fee 双 NULL 仍开仓；cap=10 截断；
// 同键 active 去重。
func TestD465_StepNullAndCap(t *testing.T) {
	ctx := context.Background()
	sc := NewCarryUniverseScanner(mtmFixture())
	st := newFakeMtmPaperStore()
	now := time.Now()
	es := NewEntryStreak()
	var opened []string
	for i := 0; i < PaperEntryStreakRounds; i++ {
		o, _, err := sc.StepPaperMtmCarry(ctx, st, es, now, nil)
		if err != nil {
			t.Fatal(err)
		}
		opened = o
	}
	if len(opened) != 1 {
		t.Fatalf("opened=%v", opened)
	}
	if st.rows[1].EntryBasisBps != nil || st.rows[1].EstRoundTripFeeUSD != nil {
		t.Fatal("tick 缺时 basis/fee 应双 NULL")
	}
	// 去重：同键 active 行在 → 再跑不开仓。
	o, _, err := sc.StepPaperMtmCarry(ctx, st, es, now, nil)
	if err != nil || len(o) != 0 {
		t.Fatalf("dedupe: opened=%v err=%v", o, err)
	}
	// cap 截断：预填 PaperMtmMaxActive 个 active 行 → 其他 canonical 也不开。
	st2 := newFakeMtmPaperStore()
	for i := 0; i < PaperMtmMaxActive; i++ {
		st2.rows[int64(i+1)] = &pgstore.PaperSpotCarry{
			ID: int64(i + 1), VenueA: "x", VenueB: "y", Symbol: string(rune('A' + i)),
			Side: "long_a_short_b", Structure: "cfd_cfd", Status: "active", EntryTs: now,
		}
	}
	o, _, err = sc.StepPaperMtmCarry(ctx, st2, NewEntryStreak(), now, nil)
	if err != nil || len(o) != 0 {
		t.Fatalf("cap: opened=%v err=%v", o, err)
	}
	// floor 边界：net<5 拒开——fixture 利差塌缩场景（上例 sc2 语义复用）。
	scFloor := NewCarryUniverseScanner(&fakeScanStore{
		facts: []fact.Fact{
			{Kind: "swap_long", Venue: "mtA", Symbol: "XAUUSD.a", Value: -1, Ts: now},
			{Kind: "swap_short", Venue: "mtA", Symbol: "XAUUSD.a", Value: 0.5, Ts: now},
			{Kind: "swap_long", Venue: "mtB", Symbol: "XAUUSD.b", Value: -0.5, Ts: now},
			{Kind: "swap_short", Venue: "mtB", Symbol: "XAUUSD.b", Value: 1.0, Ts: now},
		},
		mappings: mtmFixture().mappings,
	})
	st3 := newFakeMtmPaperStore()
	o, _, err = scFloor.StepPaperMtmCarry(ctx, st3, NewEntryStreak(), now, nil)
	if err != nil || len(o) != 0 || len(st3.rows) != 0 {
		t.Fatalf("floor: opened=%v rows=%d err=%v", o, len(st3.rows), err)
	}
}

// 费率源 exchange_configs.taker_fee_bps（fake 回旧钉值等价——okx 10/5 族）。
func (f *fakeMtmPaperStore) ExchangeTakerFeeBps(_ context.Context, venue, venueType string) (float64, bool, error) {
	fees := map[string]struct{ spot, perp float64 }{
		"binance": {10.0, 4.5}, "okx": {10.0, 5.0}, "gate": {10.0, 5.0}, "mexc": {5.0, 2.0},
	}
	t, ok := fees[venue]
	if !ok {
		return 0, false, nil
	}
	if venueType == "spot" {
		return t.spot, true, nil
	}
	return t.perp, true, nil
}

// CountDataLostExits D-042：fake 实现（冷却测试注入计数用；默认零）。
func (f *fakeMtmPaperStore) CountDataLostExits(ctx context.Context, venueA, venueB, symbol string, since time.Time) (int, error) {
	return 0, nil
}
