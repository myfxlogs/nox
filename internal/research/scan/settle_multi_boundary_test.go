package scan

// S1 结算对齐累积（跨多界/翻号序列/与快照外推差异/缺事实 fail-closed）、
// S2 分腿真费（SKLUSDT 四腿量级）、S3 逆向当轮退、S4 miss→data_lost、graduate 排 data_lost。
import (
	"context"
	"math"
	"testing"
	"time"

	"arb/internal/data/fact"
	"arb/internal/store/pgstore"
)

// factsStore 注入式 PaperFactSource。
type factsStore struct{ facts map[string][]fact.Fact }

func (s *factsStore) LatestFacts(_ context.Context, kind, venue, symbol string) ([]fact.Fact, error) {
	return s.facts[kind+"|"+venue+"|"+symbol], nil
}
func (s *factsStore) FactsSince(_ context.Context, kind, venue, symbol string, since time.Time) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, f := range s.facts[kind+"|"+venue+"|"+symbol] {
		if !f.Ts.Before(since) {
			out = append(out, f)
		}
	}
	return out, nil
}
func (s *factsStore) ListSymbolMappings(context.Context, string) ([]pgstore.SymbolMapping, error) {
	return nil, nil
}
func (s *factsStore) TicksWindow(context.Context, string, string, time.Time) ([]pgstore.TickRow, error) {
	return nil, nil
}

func settleFacts(venue, sym string, ns time.Time, ivSec int64, hist map[time.Time]float64, cur float64) *factsStore {
	m := map[string][]fact.Fact{
		fact.KindFundingNextSettle + "|" + venue + "|" + sym: {{Kind: fact.KindFundingNextSettle, Venue: venue, Symbol: sym, Value: float64(ns.Unix()), Ts: ns}},
		fact.KindFundingInterval + "|" + venue + "|" + sym:   {{Kind: fact.KindFundingInterval, Venue: venue, Symbol: sym, Value: float64(ivSec), Ts: ns}},
		fact.KindFunding + "|" + venue + "|" + sym:           {{Kind: fact.KindFunding, Venue: venue, Symbol: sym, Value: cur, Ts: ns}},
	}
	var hs []fact.Fact
	for ts, v := range hist {
		hs = append(hs, fact.Fact{Kind: fact.KindFundingHistory, Venue: venue, Symbol: sym, Value: v, Ts: ts})
	}
	m[fact.KindFundingHistory+"|"+venue+"|"+sym] = hs
	return &factsStore{facts: m}
}

// TestD474A_SettleMultiBoundary S1：跨 3 个 8h 边界，实率 10/20/-30% 逐期记，
// 与快照外推（单一当期率×3 期）结果差异证明结算语义生效。
func TestD474A_SettleMultiBoundary(t *testing.T) {
	base := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	now := base.Add(24 * time.Hour) // 次日 00:00，边界 08/16/24 已结
	ns := now.Add(8 * time.Hour)    // 下界 8:00
	iv := int64(8 * 3600)
	hist := map[time.Time]float64{
		base.Add(8 * time.Hour):  10,
		base.Add(16 * time.Hour): 20,
		now:                      -30, // 第三边界=now 整点
	}
	st := settleFacts("binance", "BTCUSDT", ns, iv, hist, 99 /*当期率应不被使用——历史全覆盖*/)
	leg := PaperLegAccrue{Kind: fact.KindFunding, Venue: "binance", Symbol: "BTCUSDT", SideFactor: 1, CurRate: 99, CurOK: true}
	add, to, ok, err := AccruePaperLegs(context.Background(), st, []PaperLegAccrue{leg}, base, now, 1000)
	if err != nil || !ok {
		t.Fatalf("acc ok=%v err=%v", ok, err)
	}
	// 每期=率/100×qty×(iv/年)：ppy=1095 → 率/100×1000/1095。
	ppy := 365.0 * 24 / 8
	want := (10.0 + 20.0 - 30.0) / 100 * 1000 / ppy
	if math.Abs(add-want) > 1e-9 {
		t.Fatalf("add=%v want=%v（逐期实率求和）", add, want)
	}
	if !to.Equal(now) {
		t.Fatalf("settledTo=%v want %v", to, now)
	}
	// 快照外推对照：当期率 99% × 3 期 → 差异存在即证明非外推。
	snap := 99.0 / 100 * 1000 / ppy * 3
	if math.Abs(add-snap) < 1e-9 {
		t.Fatalf("结算对齐结果=%v 与快照外推 %v 无差异——语义未生效", add, snap)
	}
}

// TestD474A_SettleHistMiss 历史缺界回落当期率（CurOK）；双缺 fail-closed 水位不动。
func TestD474A_SettleHistMiss(t *testing.T) {
	base := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	now := base.Add(16 * time.Hour) // 界 08:00+16:00 两界
	ns := now.Add(8 * time.Hour)
	// 历史只覆第一界（08:00），无 16:00 界——16 界回落当期率 12。
	st := settleFacts("okx", "X-USDT", ns, 8*3600, map[time.Time]float64{base.Add(8 * time.Hour): 10}, 12)
	leg := PaperLegAccrue{Kind: fact.KindFunding, Venue: "okx", Symbol: "X-USDT", SideFactor: -1, CurRate: 12, CurOK: true}
	add, to, ok, err := AccruePaperLegs(context.Background(), st, []PaperLegAccrue{leg}, base, now, 1000)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	ppy := 365.0 * 24 / 8
	want := -(10.0 + 12.0) / 100 * 1000 / ppy // sideFactor=-1
	if math.Abs(add-want) > 1e-9 {
		t.Fatalf("add=%v want=%v", add, want)
	}
	if !to.Equal(now) {
		t.Fatalf("settledTo=%v want %v", to, now)
	}
	// 缺率双缺（CurOK=false 且无历史界）→ fail-closed
	leg2 := leg
	leg2.CurOK = false
	st2 := settleFacts("okx", "X-USDT", ns, 8*3600, map[time.Time]float64{base.Add(8 * time.Hour): 10}, 0)
	// 只留一界+另一界无历史 → false
	add2, to2, ok2, err := AccruePaperLegs(context.Background(), st2, []PaperLegAccrue{leg2}, base.Add(9*time.Hour), now, 1000)
	_ = to2
	if err != nil || ok2 || add2 != 0 || to2.Equal(now) {
		t.Fatalf("双缺 fail-closed: add=%v to=%v ok=%v err=%v", add2, to2, ok2, err)
	}
	// next_settle fact 缺 → fail-closed
	st3 := &factsStore{facts: map[string][]fact.Fact{}}
	_, _, ok3, err := AccruePaperLegs(context.Background(), st3, []PaperLegAccrue{leg}, base, now, 1000)
	if err != nil || ok3 {
		t.Fatalf("缺 next_settle 应 fail-closed: ok=%v err=%v", ok3, err)
	}
}

// TestD474A_SwapLeg MT swap 日界+triple day+实收优先路径。
func TestD474A_SwapLeg(t *testing.T) {
	base := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC) // 周一 00:00
	now := base.Add(4 * 24 * time.Hour)                  // 周五 00:00：4 个日界
	facts := map[string][]fact.Fact{
		fact.KindSwapLong + "|mt|XAUUSD.a":      {{Kind: fact.KindSwapLong, Venue: "mt", Symbol: "XAUUSD.a", Value: 100 /*bps_daily*/, Ts: base}},
		fact.KindTripleSwapDay + "|mt|XAUUSD.a": {{Kind: fact.KindTripleSwapDay, Venue: "mt", Symbol: "XAUUSD.a", Value: 3 /*周三*/, Ts: base}},
	}
	st := &factsStore{facts: facts}
	leg := PaperLegAccrue{Kind: fact.KindSwapLong, Venue: "mt", Symbol: "XAUUSD.a", SideFactor: 1}
	add, to, ok, err := AccruePaperLegs(context.Background(), st, []PaperLegAccrue{leg}, base, now, 1000)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	// 4 日界（周二~周五界）：周三界 ×3 → 共 6×(100/1e4×1000)=6×10=60。
	if math.Abs(add-60) > 1e-9 {
		t.Fatalf("add=%v want 60（周三界×3）", add)
	}
	if !to.Equal(now) {
		t.Fatalf("settledTo=%v want %v", to, now)
	}
}

// TestD474A_SpotCarryFees S2：spot carry 四腿费（SKLUSDT 审计锚 ~$2.9 量级，
// 显著高于旧约 $1.0 单腿往返估）。
func TestD474A_SpotCarryFees(t *testing.T) {
	f := paperSpotCarryFeeUSD(context.Background(), newFakeMtmPaperStore(), "binance", 1000)
	if f == nil {
		t.Fatal("binance 已覆盖不应 nil")
	}
	if *f < 2.0 {
		t.Fatalf("binance 四笔费=%v 应显著高于旧 ~$1.0（2 现货腿+2 perp腿）", *f)
	}
	if *f > 5.0 {
		t.Fatalf("binance 四笔费=%v 异常高", *f)
	}
	// 未覆盖 venue → nil（非 0）
	if g := paperSpotCarryFeeUSD(context.Background(), newFakeMtmPaperStore(), "kraken", 1000); g != nil {
		t.Fatalf("未覆盖 venue 应 nil 得 %v", *g)
	}
	// perp×perp 双腿
	if h := PaperPerpPerpFeeUSD(context.Background(), newFakeMtmPaperStore(), "binance", "okx", 1000); h == nil || *h < 1.0 {
		t.Fatalf("perp×perp 费=%v", h)
	}
}

// TestD474A_SpotAdverseExit S3：funding 翻号当轮退 adverse。
func TestD474A_SpotAdverseExit(t *testing.T) {
	st := newFakeMtmPaperStore()
	st.facts = map[string][]fact.Fact{}
	now := time.Now()
	entry := now.Add(-48 * time.Hour)
	st.rows[1] = &pgstore.PaperSpotCarry{
		ID: 1, VenueA: "binance", Symbol: "BTCUSDT", Side: "short_perp",
		Status: "active", EntryTs: entry, QtyUSD: 1000, EntryFundingAnn: 50,
	}
	// FundingAnn 翻负（short_perp 收息转付息）→ 当轮退
	f := -30.0
	st.universe = map[string]pgstore.CexUniverseRow{
		"binance|BTCUSDT": {Venue: "binance", Symbol: "BTCUSDT", FundingAnn: &f, Status: "TRADING", Tier: "T1"},
	}
	es := NewEntryStreak()
	_, exited, err := StepPaperSpotCarry(context.Background(), st, es, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if exited != 1 || len(st.exits) != 1 || st.exits[0].reason != "adverse" {
		t.Fatalf("exited=%d exits=%+v want 1×adverse", exited, st.exits)
	}
	// 同键反向行可重开（funding<0 → long_perp；universe 行达标 → 新开仓）
	st.facts = map[string][]fact.Fact{}
	vol := 2e7
	st.universe["binance|BTCUSDT"] = pgstore.CexUniverseRow{
		Venue: "binance", Symbol: "BTCUSDT", FundingAnn: &f, Status: "TRADING", Tier: "T1",
		Vol24h: &vol, FirstSeen: now.Add(-72 * time.Hour),
	}
	opened, _, err := StepPaperSpotCarry(context.Background(), st, es, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	// entryStreak 需 PaperEntryStreakRounds 轮——跑满轮数
	for i := 0; i < PaperEntryStreakRounds; i++ {
		opened, _, _ = StepPaperSpotCarry(context.Background(), st, es, now, nil)
	}
	if len(opened) == 0 {
		var sawNew bool
		for _, r := range st.rows {
			if r.Side == "long_perp" && r.Status == "active" {
				sawNew = true
			}
		}
		if !sawNew {
			t.Fatal("adverse 退后同键反向行未能重开")
		}
	}
}

// TestD474A_SpotMissDataLost S4：缺席不计时，3 连缺 → data_lost；复出清 miss。
func TestD474A_SpotMissDataLost(t *testing.T) {
	st := newFakeMtmPaperStore()
	st.facts = map[string][]fact.Fact{}
	now := time.Now()
	st.rows[1] = &pgstore.PaperSpotCarry{
		ID: 1, VenueA: "binance", Symbol: "BTCUSDT", Side: "short_perp",
		Status: "active", EntryTs: now.Add(-48 * time.Hour), QtyUSD: 1000,
	}
	st.universe = map[string]pgstore.CexUniverseRow{} // 行缺席
	es := NewEntryStreak()
	for i := 1; i <= paperMissExitRounds; i++ {
		_, exited, err := StepPaperSpotCarry(context.Background(), st, es, now, nil)
		if err != nil {
			t.Fatal(err)
		}
		if i < paperMissExitRounds {
			if exited != 0 || st.rows[1].MissStreak != i || st.rows[1].LowStreak != 0 {
				t.Fatalf("第%d缺席: exited=%d miss=%d low=%d want miss=%d low=0", i, exited, st.rows[1].MissStreak, st.rows[1].LowStreak, i)
			}
		} else {
			if exited != 1 || st.exits[len(st.exits)-1].reason != "data_lost" {
				t.Fatalf("第%d缺席应 data_lost: exited=%d exits=%+v", i, exited, st.exits)
			}
		}
	}
}

// TestD474A_GraduateReasons data_lost 不得毕业；adverse 行保留资格。
func TestD474A_GraduateReasons(t *testing.T) {
	now := time.Now()
	exited := now.Add(-time.Hour)
	fee, conv := 1.0, 0.5
	pos := pgstore.PaperSpotCarry{AccruedUSD: 50, EstRoundTripFeeUSD: &fee, ConvergenceUSD: &conv}
	for _, tc := range []struct {
		reason string
		want   bool
	}{{"normalized", true}, {"age_7d", true}, {"adverse", true}, {"data_lost", false}, {"promoted_demo", false}} {
		p := pos
		p.ExitTs = &exited
		p.ExitReason = tc.reason
		p.EntryTs = now.Add(-144 * time.Hour) // 持仓时长≥GradMinAgeDays=5d
		p.Status = "exited"
		if got := graduationQualified(p, now); got != tc.want {
			t.Fatalf("reason=%s eligible=%v want %v", tc.reason, got, tc.want)
		}
	}
}
