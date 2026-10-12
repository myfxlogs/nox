package scan

// D-463 T2：blocked 旗标拦纸面入口 + warn 不拦 + 毕业建议 flag_warn 透出。
import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

func flagCand() *fakeMtmPaperStore {
	funding, vol := 30.0, 2e7
	return &fakeMtmPaperStore{
		rows:  map[int64]*pgstore.PaperSpotCarry{},
		ticks: map[string][]pgstore.TickRow{},
		universe: map[string]pgstore.CexUniverseRow{
			"binance|GUSDT": {
				Venue: "binance", Symbol: "GUSDT", Status: "TRADING", Tier: "T1",
				FundingAnn: &funding, Vol24h: &vol, FirstSeen: time.Now().Add(-72 * time.Hour),
			},
		},
	}
}

// TestD463_BlockedFlagSkipsOpen blocked 命中→候选跳过；warn 不拦（纸面=免费复核通道）。
func TestD463_BlockedFlagSkipsOpen(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	// blocked：灌满 streak 仍不开。
	st := flagCand()
	flags := map[string]pgstore.VenueSymbolFlag{
		pgstore.FlagKey("binance", "GUSDT"): {Venue: "binance", Symbol: "GUSDT", Level: "blocked", Reason: "GUSDT 字段语义坑待查"},
	}
	opened, _, err := StepPaperSpotCarry(ctx, st, eligibleStreak("binance|GUSDT"), now, flags)
	if err != nil || len(opened) != 0 || len(st.rows) != 0 {
		t.Fatalf("blocked: opened=%v rows=%d err=%v", opened, len(st.rows), err)
	}
	// warn：同键 warn 级→照常开。
	st2 := flagCand()
	flags[pgstore.FlagKey("binance", "GUSDT")] = pgstore.VenueSymbolFlag{
		Venue: "binance", Symbol: "GUSDT", Level: "warn", Reason: "funding 语义待查"}
	opened, _, err = StepPaperSpotCarry(ctx, st2, eligibleStreak("binance|GUSDT"), now, flags)
	if err != nil || len(opened) != 1 {
		t.Fatalf("warn: opened=%v err=%v", opened, err)
	}
}

// TestD463_GraduationFlagWarn warn 命中→建议行 flag_warn 透出+告警文案带旗；
// blocked 级不参与 warn 透出；无旗行 NULL。
func TestD463_GraduationFlagWarn(t *testing.T) {
	row := gradPaper(2, "active", 6, 0, 10, 0, "") // venue="1314615×gate" symbol="BTC"
	fs := &fakeGradStore{rows: []pgstore.PaperSpotCarry{row}, existing: map[int64]bool{}}
	flags := map[string]pgstore.VenueSymbolFlag{
		pgstore.FlagKey("gate", "BTC"):    {Venue: "gate", Symbol: "BTC", Level: "warn", Reason: "funding 语义待查"},
		pgstore.FlagKey("1314615", "BTC"): {Venue: "1314615", Symbol: "BTC", Level: "blocked", Reason: "不应透出"},
	}
	n, err := StepCarryGraduation(context.Background(), fs, time.Now(), flags)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	g := fs.inserted[0]
	if g.FlagWarn == nil || *g.FlagWarn != "⚠funding 语义待查" {
		t.Fatalf("flag_warn=%v", g.FlagWarn)
	}
	if !strings.Contains(fs.alerts[0], "⚠funding 语义待查") {
		t.Fatalf("alert 缺红旗: %s", fs.alerts[0])
	}
	// 无旗 → NULL。
	fs2 := &fakeGradStore{rows: []pgstore.PaperSpotCarry{row}, existing: map[int64]bool{}}
	if _, err := StepCarryGraduation(context.Background(), fs2, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if fs2.inserted[0].FlagWarn != nil {
		t.Fatal("无旗行 flag_warn 应 NULL")
	}
}

// TestD463_BlockedFlagHitLegs 返修 R1 helper 矩阵：×腿/全串/MtVenue 各自命中；
// warn 级/异 symbol/异 venue 不误拦；nil flags 放行。
func TestD463_BlockedFlagHitLegs(t *testing.T) {
	mt := "1320224"
	flag := func(v, s, level string) pgstore.VenueSymbolFlag {
		return pgstore.VenueSymbolFlag{Venue: v, Symbol: s, Level: level, Reason: "r"}
	}
	cases := []struct {
		name    string
		venue   string
		symbol  string
		mtVenue *string
		flags   map[string]pgstore.VenueSymbolFlag
		wantHit bool
	}{
		{"×腿命中", "1314615×gate", "BTC", nil, map[string]pgstore.VenueSymbolFlag{
			pgstore.FlagKey("gate", "BTC"): flag("gate", "BTC", "blocked")}, true},
		{"全串命中", "1314615×gate", "BTC", nil, map[string]pgstore.VenueSymbolFlag{
			pgstore.FlagKey("1314615×gate", "BTC"): flag("1314615×gate", "BTC", "blocked")}, true},
		{"MtVenue命中", "gate", "BTC", &mt, map[string]pgstore.VenueSymbolFlag{
			pgstore.FlagKey("1320224", "BTC"): flag("1320224", "BTC", "blocked")}, true},
		{"异venue不拦", "1314615×gate", "BTC", nil, map[string]pgstore.VenueSymbolFlag{
			pgstore.FlagKey("okx", "BTC"): flag("okx", "BTC", "blocked")}, false},
		{"异symbol不拦", "1314615×gate", "BTC", nil, map[string]pgstore.VenueSymbolFlag{
			pgstore.FlagKey("gate", "ETH"): flag("gate", "ETH", "blocked")}, false},
		{"warn级不拦", "1314615×gate", "BTC", nil, map[string]pgstore.VenueSymbolFlag{
			pgstore.FlagKey("gate", "BTC"): flag("gate", "BTC", "warn")}, false},
		{"nil放行", "1314615×gate", "BTC", nil, nil, false},
	}
	for _, tc := range cases {
		va, vb, _ := strings.Cut(tc.venue, "×")
		if got := blockedFlagHit(va, vb, tc.symbol, tc.mtVenue, tc.flags); (got != nil) != tc.wantHit {
			t.Fatalf("%s: hit=%v want=%v", tc.name, got != nil, tc.wantHit)
		}
	}
}

// TestD463_SpotLegFlagBlocks 返修 R1：spot 入口腿级命中——universe venue 裸域单列
// （D-499 生产实证 2350 行零 ×，配对假设不成立），旗标挂 gate|GUSDT 拦下。
func TestD463_SpotLegFlagBlocks(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	funding, vol := 30.0, 2e7
	st := &fakeMtmPaperStore{
		rows:  map[int64]*pgstore.PaperSpotCarry{},
		ticks: map[string][]pgstore.TickRow{},
		universe: map[string]pgstore.CexUniverseRow{
			"gate|GUSDT": {
				Venue: "gate", Symbol: "GUSDT", Status: "TRADING", Tier: "T1",
				FundingAnn: &funding, Vol24h: &vol, FirstSeen: now.Add(-72 * time.Hour),
			},
		},
	}
	flags := map[string]pgstore.VenueSymbolFlag{
		pgstore.FlagKey("gate", "GUSDT"): {Venue: "gate", Symbol: "GUSDT", Level: "blocked", Reason: "腿级旗标"},
	}
	opened, _, err := StepPaperSpotCarry(ctx, st, eligibleStreak("gate|GUSDT"), now, flags)
	if err != nil || len(opened) != 0 || len(st.rows) != 0 {
		t.Fatalf("leg blocked: opened=%v rows=%d err=%v", opened, len(st.rows), err)
	}
}

// TestD463_CfdLegFlagBlocks 返修 R2：cfd 入口腿级命中——venueCombo="mt×cex"，
// 旗标挂 cex 单腿→top-N 拦；异 venue 旗不误拦（对照组照开，隔离拦截因）。
func TestD463_CfdLegFlagBlocks(t *testing.T) {
	ctx := context.Background()
	rows := []UniverseRow{d374Row("SOL", "277842155", "binance", "long_cfd_short_perp", 10.95, "")}
	st := newFakeMtmPaperStore()
	flags := map[string]pgstore.VenueSymbolFlag{
		pgstore.FlagKey("binance", "SOL"): {Venue: "binance", Symbol: "SOL", Level: "blocked", Reason: "腿级旗标"},
	}
	opened, _, err := StepPaperCfdCarry(ctx, st, rows, time.Now(), d410Opts(), flags)
	if err != nil || len(opened) != 0 || len(st.rows) != 0 {
		t.Fatalf("leg blocked: opened=%v rows=%d err=%v", opened, len(st.rows), err)
	}
	// 对照：无关 venue 旗→照开。D-144 ③：簿可覆盖前置（mt tick 种子）。
	st2 := newFakeMtmPaperStore()
	st2.ticks["277842155|SOL"] = []pgstore.TickRow{{Ts: time.Now(), Venue: "277842155", Symbol: "SOL", Bid: 1, Ask: 1}}
	opened, _, err = StepPaperCfdCarry(ctx, st2, rows, time.Now(), d410Opts(), map[string]pgstore.VenueSymbolFlag{
		pgstore.FlagKey("okx", "SOL"): {Venue: "okx", Symbol: "SOL", Level: "blocked", Reason: "异 venue"},
	})
	if err != nil || len(opened) != 1 || len(st2.rows) != 1 {
		t.Fatalf("control: opened=%v rows=%d err=%v", opened, len(st2.rows), err)
	}
}

// TestD463_MtmLegFlagBlocks 返修 R2：mtm 入口腿级命中——pair venue="mtA×mtB"，
// 旗标挂单腿 mtB|XAU→streak 灌满仍不开；warn 级对照照开（拦截因=blocked 非候选无效）。
func TestD463_MtmLegFlagBlocks(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	mkStore := func() *fakeMtmPaperStore {
		st := newFakeMtmPaperStore()
		st.ticks["mtA|XAUUSD.a"] = []pgstore.TickRow{{Ts: now.Add(-time.Minute), Venue: "mtA", Symbol: "XAUUSD.a", Bid: 100, Ask: 100.05}}
		st.ticks["mtB|XAUUSD.b"] = []pgstore.TickRow{{Ts: now.Add(-time.Minute), Venue: "mtB", Symbol: "XAUUSD.b", Bid: 99.9, Ask: 99.95}}
		return st
	}
	sc := NewCarryUniverseScanner(mtmFixture())
	st := mkStore()
	flags := map[string]pgstore.VenueSymbolFlag{
		pgstore.FlagKey("mtB", "XAU"): {Venue: "mtB", Symbol: "XAU", Level: "blocked", Reason: "腿级旗标"},
	}
	es := NewEntryStreak()
	for i := 0; i < PaperEntryStreakRounds; i++ {
		opened, _, err := sc.StepPaperMtmCarry(ctx, st, es, now, flags)
		if err != nil || len(opened) != 0 || len(st.rows) != 0 {
			t.Fatalf("round %d leg blocked: opened=%v rows=%d err=%v", i, opened, len(st.rows), err)
		}
	}
	// 对照：同候选 warn 级→streak 灌满照常开。
	st2 := mkStore()
	warn := map[string]pgstore.VenueSymbolFlag{
		pgstore.FlagKey("mtB", "XAU"): {Venue: "mtB", Symbol: "XAU", Level: "warn", Reason: "warn 不拦"},
	}
	es2 := NewEntryStreak()
	var opened []string
	var err error
	for i := 0; i < PaperEntryStreakRounds; i++ {
		opened, _, err = sc.StepPaperMtmCarry(ctx, st2, es2, now, warn)
		if err != nil {
			t.Fatalf("control round %d: %v", i, err)
		}
	}
	if len(opened) != 1 {
		t.Fatalf("control: opened=%v want 1", opened)
	}
}
