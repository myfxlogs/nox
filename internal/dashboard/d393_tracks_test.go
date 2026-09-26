// d393_tracks_test.go —— D-393：纸面仓卡片分轨重组锚（A 轨级汇总/B 行字段透出/E days_held 存活口径）。
// 红证：基线无 tracks 字段、row 无 structure/mt_venue、exited days_held 用 now−entry_ts（活计时器）。
package dashboard

import (
	"context"
	"math"
	"testing"
	"time"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
)

func d393Now() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC) }

// TestD393_TrackAggregation 锚 A：多轨混合 fixture，逐字段断言轨级汇总；
// 覆盖 win_rate 两分支（total_pnl 优先 / accrued 兜底）。
func TestD393_TrackAggregation(t *testing.T) {
	now := d393Now()
	exitA := now.Add(-24 * time.Hour) // entry 5d 前，exit 1d 前 → 存活 4d
	exitB := now.Add(-48 * time.Hour) // entry 6d 前，exit 2d 前 → 存活 4d
	exitC := now.Add(-12 * time.Hour) // entry 3d 前 → 存活 2.5d
	exitPP := now.Add(-6 * time.Hour) // entry 2d 前 → 存活 1.75d
	fake := &fakePaperBoardSource{rows: []pgstore.PaperSpotCarry{
		// spot_carry 轨：active2 + demo1 + exited3
		{ID: 1, VenueA: "binance", Symbol: "A1", Side: "short_perp", Status: "active",
			QtyUSD: 1000, EntryTs: now.Add(-36 * time.Hour), AccruedUSD: 10},
		{ID: 2, VenueA: "binance", Symbol: "A2", Side: "short_perp", Status: "active",
			QtyUSD: 500, EntryTs: now.Add(-24 * time.Hour), AccruedUSD: 5},
		{ID: 3, VenueA: "binance", Symbol: "D1", Side: "short_perp", Status: "demo",
			QtyUSD: 200, EntryTs: now.Add(-12 * time.Hour), AccruedUSD: 1},
		// exited：total_pnl 优先分支——accrued 负但 total 正 → win
		{ID: 4, VenueA: "binance", Symbol: "W1", Side: "short_perp", Status: "exited",
			QtyUSD: 1000, EntryTs: now.Add(-5 * 24 * time.Hour), ExitTs: &exitA, AccruedUSD: -1,
			EntryBasisBps: boardF64(60), LastBasisBps: boardF64(10)}, // total=−1+5−3=+1 → win
		// accrued 兜底分支——无基差 → win=accrued：+2 → win
		{ID: 5, VenueA: "binance", Symbol: "W2", Side: "short_perp", Status: "exited",
			QtyUSD: 1000, EntryTs: now.Add(-6 * 24 * time.Hour), ExitTs: &exitB, AccruedUSD: 2},
		// accrued 兜底亏：−0.5 → lose
		{ID: 6, VenueA: "binance", Symbol: "L1", Side: "short_perp", Status: "exited",
			QtyUSD: 1000, EntryTs: now.Add(-3 * 24 * time.Hour), ExitTs: &exitC, AccruedUSD: -0.5},
		// perp_perp 轨：active1 + exited1（accrued 兜底亏）
		{ID: 7, VenueA: "okx", Symbol: "P1", Side: "short_perp", Status: "active",
			QtyUSD: 800, EntryTs: now.Add(-24 * time.Hour), AccruedUSD: 3, Structure: "perp_perp"},
		{ID: 8, VenueA: "okx", Symbol: "P2", Side: "long_perp", Status: "exited",
			QtyUSD: 800, EntryTs: now.Add(-2 * 24 * time.Hour), ExitTs: &exitPP, AccruedUSD: -0.2, Structure: "perp_perp"},
	}}
	resp, err := buildPaperSpotCarryBoard(context.Background(), fake, now)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(resp.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2（仅非空轨）", len(resp.Tracks))
	}
	// 固定序：spot_carry 在 perp_perp 前
	if resp.Tracks[0].Structure != "spot_carry" || resp.Tracks[1].Structure != "perp_perp" {
		t.Fatalf("轨序 = %s,%s，want spot_carry,perp_perp", resp.Tracks[0].Structure, resp.Tracks[1].Structure)
	}
	sc := resp.Tracks[0]
	if sc.ActiveN != 2 || sc.DemoN != 1 || sc.ExitedN != 3 {
		t.Errorf("spot_carry 计数 = %d/%d/%d, want 2/1/3", sc.ActiveN, sc.DemoN, sc.ExitedN)
	}
	if math.Abs(sc.AccruedHoldingUsd-16) > 1e-9 { // 10+5+1
		t.Errorf("accrued_holding = %v, want 16", sc.AccruedHoldingUsd)
	}
	if math.Abs(sc.NotionalHoldingUsd-1700) > 1e-9 { // 1000+500+200
		t.Errorf("notional_holding = %v, want 1700", sc.NotionalHoldingUsd)
	}
	if math.Abs(sc.ExitedAccruedSumUsd-0.5) > 1e-9 { // −1+2−0.5
		t.Errorf("exited_accrued = %v, want 0.5", sc.ExitedAccruedSumUsd)
	}
	// win：W1 total+1 → win；W2 accrued+2 → win；L1 accrued−0.5 → lose → 2/3
	if math.Abs(sc.WinRatePct-200.0/3) > 0.01 {
		t.Errorf("win_rate = %v, want ≈66.67（total 优先 +1 胜 / accrued 兜底 +2 胜 −0.5 负）", sc.WinRatePct)
	}
	// avg_exit_days=(4+4+2.5)/3=3.5
	if math.Abs(sc.AvgExitDays-3.5) > 0.01 {
		t.Errorf("avg_exit_days = %v, want 3.5", sc.AvgExitDays)
	}
	pp := resp.Tracks[1]
	if pp.ActiveN != 1 || pp.ExitedN != 1 || pp.WinRatePct != 0 {
		t.Errorf("perp_perp = %+v, want active1/exited1/win0", pp)
	}
	if math.Abs(pp.AvgExitDays-1.75) > 0.01 {
		t.Errorf("perp_perp avg_exit_days = %v, want 1.75", pp.AvgExitDays)
	}
}

// TestD393_RowFields 锚 B：行透出 structure/mt_venue；历史空串→spot_carry；
// perp_perp NULL mt_venue 诚实缺省。
func TestD393_RowFields(t *testing.T) {
	now := d393Now()
	mt := "mt5-live"
	fake := &fakePaperBoardSource{rows: []pgstore.PaperSpotCarry{
		{ID: 1, VenueA: "binance", Symbol: "S1", Side: "short_perp", Status: "active",
			QtyUSD: 1000, EntryTs: now.Add(-24 * time.Hour)}, // Structure="" 历史行
		{ID: 2, VenueA: "mt5", Symbol: "C1", Side: "long_cfd_short_perp", Status: "active",
			QtyUSD: 1000, EntryTs: now.Add(-24 * time.Hour), Structure: "long_cfd_short_perp", MtVenue: &mt},
		{ID: 3, VenueA: "okx", Symbol: "P1", Side: "short_perp", Status: "active",
			QtyUSD: 1000, EntryTs: now.Add(-24 * time.Hour), Structure: "perp_perp"}, // MtVenue nil
	}}
	resp, err := buildPaperSpotCarryBoard(context.Background(), fake, now)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	bySym := map[string]*v1.PaperSpotCarryRow{}
	for _, r := range resp.Rows {
		bySym[r.Symbol] = r
	}
	if bySym["S1"].Structure != "spot_carry" {
		t.Errorf("历史空串 structure = %q, want spot_carry 归一", bySym["S1"].Structure)
	}
	if bySym["C1"].Structure != "long_cfd_short_perp" || bySym["C1"].MtVenue == nil || *bySym["C1"].MtVenue != "mt5-live" {
		t.Errorf("CFD 行 = %q/%v, want long_cfd_short_perp/mt5-live", bySym["C1"].Structure, bySym["C1"].MtVenue)
	}
	if bySym["P1"].Structure != "perp_perp" || bySym["P1"].MtVenue != nil {
		t.Errorf("perp_perp 行 = %q/%v, want perp_perp + mt_venue nil", bySym["P1"].Structure, bySym["P1"].MtVenue)
	}
}

// TestD393_ExitedDaysHeld 锚 E：exited 行 days_held 冻结在 exit_ts−entry_ts，
// 不随 now 增长；realized_ann_pct 用同一存活天数重算。
// 基线红：旧口径 now−entry_ts → days_held=7 而非 5，且每过一天继续涨。
func TestD393_ExitedDaysHeld(t *testing.T) {
	now := d393Now()
	exit := now.Add(-48 * time.Hour) // entry 7d 前、exit 2d 前 → 存活恰 5d
	fake := &fakePaperBoardSource{rows: []pgstore.PaperSpotCarry{
		{ID: 1, VenueA: "binance", Symbol: "X1", Side: "short_perp", Status: "exited",
			QtyUSD: 1000, EntryTs: now.Add(-7 * 24 * time.Hour), ExitTs: &exit, AccruedUSD: 5},
		{ID: 2, VenueA: "binance", Symbol: "Y1", Side: "short_perp", Status: "active",
			QtyUSD: 1000, EntryTs: now.Add(-7 * 24 * time.Hour), AccruedUSD: 5},
	}}
	resp, err := buildPaperSpotCarryBoard(context.Background(), fake, now)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	bySym := map[string]*v1.PaperSpotCarryRow{}
	for _, r := range resp.Rows {
		bySym[r.Symbol] = r
	}
	x := bySym["X1"]
	if x.DaysHeld != 5 {
		t.Errorf("exited days_held = %v, want 5（exit−entry 冻结，非 now−entry 的 7）", x.DaysHeld)
	}
	// realized_ann = 5/1000×365/5×100 = 36.5（旧口径 7 天稀释为 ≈26.07）
	if x.RealizedAnnPct == nil || math.Abs(*x.RealizedAnnPct-36.5) > 0.01 {
		t.Errorf("exited realized_ann = %v, want 36.5（5 天存活重算）", x.RealizedAnnPct)
	}
	if bySym["Y1"].DaysHeld != 7 {
		t.Errorf("active days_held = %v, want 7（now−entry 不变）", bySym["Y1"].DaysHeld)
	}
}
