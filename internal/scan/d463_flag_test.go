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
	opened, _, err := StepPaperSpotCarry(ctx, st, eligibleStreak("binance|GUSDT"), now, 5.0, flags)
	if err != nil || len(opened) != 0 || len(st.rows) != 0 {
		t.Fatalf("blocked: opened=%v rows=%d err=%v", opened, len(st.rows), err)
	}
	// warn：同键 warn 级→照常开。
	st2 := flagCand()
	flags[pgstore.FlagKey("binance", "GUSDT")] = pgstore.VenueSymbolFlag{
		Venue: "binance", Symbol: "GUSDT", Level: "warn", Reason: "funding 语义待查"}
	opened, _, err = StepPaperSpotCarry(ctx, st2, eligibleStreak("binance|GUSDT"), now, 5.0, flags)
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
