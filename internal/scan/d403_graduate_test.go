// d403_graduate_test.go —— D-403 S6：毕业判定合格矩阵（先红后绿锚）。
// 覆盖 SSOT S6：active<5d 拒/accrued≤0 拒/low_streak>0 拒/exited-fail-reason 拒/
// exited-age_7d-5d-盈利 收/同行二次扫描不重复建议/聚合告警恰一条。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

type fakeGradStore struct {
	rows     []pgstore.PaperSpotCarry
	inserted []pgstore.CarryGraduation
	existing map[int64]bool // paper_id → 已有建议（幂等模拟）
	alerts   []string
	failNext bool
}

func (f *fakeGradStore) ListPaperForGraduation(context.Context) ([]pgstore.PaperSpotCarry, error) {
	return f.rows, nil
}

func (f *fakeGradStore) InsertCarryGraduation(_ context.Context, g *pgstore.CarryGraduation) (bool, error) {
	if f.failNext {
		f.failNext = false
		return false, context.DeadlineExceeded
	}
	if f.existing[g.PaperID] {
		return false, nil
	}
	f.existing[g.PaperID] = true
	f.inserted = append(f.inserted, *g)
	return true, nil
}

func (f *fakeGradStore) InsertAlert(_ context.Context, _ *int64, level, message string) error {
	f.alerts = append(f.alerts, level+"|"+message)
	return nil
}

func gradPaper(id int64, status string, entryOffsetDays, exitOffsetDays float64, accrued float64, lowStreak int, exitReason string) pgstore.PaperSpotCarry {
	now := time.Now()
	p := pgstore.PaperSpotCarry{
		ID: id, Venue: "1314615×gate", Symbol: "BTC", Structure: "long_cfd_short_perp",
		Status: status, AccruedUSD: accrued, LowStreak: lowStreak, ExitReason: exitReason,
		EntryTs: now.Add(time.Duration(-entryOffsetDays*24) * time.Hour),
	}
	if status == "exited" {
		ts := now.Add(time.Duration(-exitOffsetDays*24) * time.Hour)
		p.ExitTs = &ts
	}
	// D-410 S5：判据升 all-in 净额——fixture 默认账齐（fee=0.5/conv=0.1 → net=accrued−0.4，
	// accrued≤0 仍拒、accrued>0 仍收），既有矩阵语义不变；NULL 拒毕业由 d410 测试覆盖。
	fee, conv := 0.5, 0.1
	p.EstRoundTripFeeUSD, p.ConvergenceUSD = &fee, &conv
	return p
}

func TestD403_Graduate_Matrix(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		row  pgstore.PaperSpotCarry
		want bool
	}{
		{"active<5d 拒", gradPaper(1, "active", 4.9, 0, 10, 0, ""), false},
		{"active-5d-盈利-收", gradPaper(2, "active", 5.1, 0, 10, 0, ""), true},
		{"active-accrued≤0 拒", gradPaper(3, "active", 9, 0, 0, 0, ""), false},
		{"active-low_streak>0 拒", gradPaper(4, "active", 9, 0, 10, 1, ""), false},
		{"exited-promoted_demo 拒", gradPaper(5, "exited", 7, 1, 10, 0, "promoted_demo"), false},
		{"exited-age_7d-持仓5d-盈利 收", gradPaper(6, "exited", 7, 0.5, 10, 0, "age_7d"), true},
		{"exited-normalized-持仓<5d 拒", gradPaper(7, "exited", 3, 0.1, 10, 0, "normalized"), false},
		{"exited-14d 窗外 拒", gradPaper(8, "exited", 20, 15, 10, 0, "normalized"), false},
		{"demo 行拒", gradPaper(9, "demo", 9, 0, 10, 0, ""), false},
	}
	for _, tc := range cases {
		fs := &fakeGradStore{rows: []pgstore.PaperSpotCarry{tc.row}, existing: map[int64]bool{}}
		n, err := StepCarryGraduation(context.Background(), fs, now, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := n == 1
		if got != tc.want {
			t.Errorf("%s: newCount=%d want建议=%v", tc.name, n, tc.want)
		}
	}
}

func TestD403_Graduate_AggregatedAlert_Once(t *testing.T) {
	fs := &fakeGradStore{
		rows:     []pgstore.PaperSpotCarry{gradPaper(2, "active", 6, 0, 10, 0, ""), gradPaper(10, "active", 7, 0, 5, 0, "")},
		existing: map[int64]bool{},
	}
	n, err := StepCarryGraduation(context.Background(), fs, time.Now(), nil)
	if err != nil || n != 2 {
		t.Fatalf("newCount=%d err=%v, want 2/nil", n, err)
	}
	if len(fs.alerts) != 1 {
		t.Fatalf("聚合告警应恰 1 条（非逐行刷屏），got %d: %v", len(fs.alerts), fs.alerts)
	}
	if !contains(fs.alerts[0], "2 个纸面行验证达标待授权") {
		t.Errorf("告警文案缺计数: %s", fs.alerts[0])
	}
}

func TestD403_Graduate_Idempotent_SecondScan(t *testing.T) {
	fs := &fakeGradStore{rows: []pgstore.PaperSpotCarry{gradPaper(2, "active", 6, 0, 10, 0, "")}, existing: map[int64]bool{}}
	if _, err := StepCarryGraduation(context.Background(), fs, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	n, err := StepCarryGraduation(context.Background(), fs, time.Now(), nil) // 同行二次扫描
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("二次扫描 newCount=%d, want 0（paper_id UNIQUE 幂等不重复建议）", n)
	}
	if len(fs.alerts) != 1 {
		t.Errorf("二次扫描不应再发告警，alerts=%d", len(fs.alerts))
	}
}

func TestD403_ResolveGraduationCanonicals(t *testing.T) {
	entries := []SymbolMapEntry{
		{Canonical: "BTCUSD.M", IsPrimary: false},
		{Canonical: "BTCUSD", IsPrimary: true},
		{Canonical: "XAUUSD", IsPrimary: true},
	}
	pair, err := ResolveGraduationCanonicals("BTC", "1279967", 1, entries)
	if err != nil {
		t.Fatal(err)
	}
	if pair.MtCanonical != "BTCUSD" || pair.CexCanonical != "BTCUSDT" || pair.MtVenueKey != "1279967:mt5" {
		t.Errorf("resolve: %+v", pair)
	}
	// 规范化命中：map 侧 XAUUSD 形 vs universe 侧 XAU
	pair, err = ResolveGraduationCanonicals("XAU", "1314615", 0, entries)
	if err != nil || pair.MtCanonical != "XAUUSD" || pair.MtVenueKey != "1314615:mt4" {
		t.Errorf("normalize resolve: %+v err=%v", pair, err)
	}
	// 映射缺失 fail-closed
	if _, err := ResolveGraduationCanonicals("SOL", "1279967", 1, entries); err == nil {
		t.Error("缺失映射应报错（fail-closed 不臆造）")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
