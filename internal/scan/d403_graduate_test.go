// d403_graduate_test.go —— D-403 S6：毕业判定合格矩阵（先红后绿锚）。
// 覆盖 SSOT S6：active<5d 拒/accrued≤0 拒/low_streak>0 拒/exited-fail-reason 拒/
// exited-age_7d-5d-盈利 收/同行二次扫描不重复建议/聚合告警恰一条。
package scan

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

type fakeGradStore struct {
	rows      []pgstore.PaperSpotCarry
	inserted  []pgstore.CarryGraduation
	existing  map[int64]bool // paper_id → 已有建议（幂等模拟）
	alerts    []string
	failNext  bool
	grads     []pgstore.CarryGraduation // D-474b S3：pending 建议存量
	decisions map[int64]string          // id → 落账 status
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

// D-474b S3：stale 回查面三件。
func (f *fakeGradStore) ListCarryGraduations(context.Context) ([]pgstore.CarryGraduation, error) {
	return f.grads, nil
}
func (f *fakeGradStore) GetPaperSpotCarryByID(_ context.Context, id int64) (*pgstore.PaperSpotCarry, error) {
	for i := range f.rows {
		if f.rows[i].ID == id {
			return &f.rows[i], nil
		}
	}
	return nil, fmt.Errorf("paper %d not found", id)
}
func (f *fakeGradStore) UpdateCarryGraduationDecision(_ context.Context, id int64, status string, _ *int64, _ string) error {
	if f.decisions == nil {
		f.decisions = map[int64]string{}
	}
	f.decisions[id] = status
	return nil
}

func gradPaper(id int64, status string, entryOffsetDays, exitOffsetDays float64, accrued float64, lowStreak int, exitReason string) pgstore.PaperSpotCarry {
	now := time.Now()
	p := pgstore.PaperSpotCarry{
		ID: id, VenueA: "1314615", VenueB: "gate", Symbol: "BTC", Structure: "long_cfd_short_perp",
		Status: status, AccruedUSD: accrued, LowStreak: lowStreak, ExitReason: exitReason,
		EntryTs: now.Add(time.Duration(-entryOffsetDays*24) * time.Hour),
	}
	if status == "exited" {
		ts := now.Add(time.Duration(-exitOffsetDays*24) * time.Hour)
		p.ExitTs = &ts
		// D-468 S2 夹具矫正（KB api-response-fields-empirical）：convergence_usd 仅退出
		// 时写——列值只给 exited 行，active 行不再撒谎塞退出时点才存在的列。
		conv := 0.1
		p.ConvergenceUSD = &conv
	}
	if status == "active" {
		// D-468 S2：active 行收敛账走 entry/last basis 现算（dir=−1：−1×(100−101)×1000/1e4
		// =+0.1，与原夹具 conv=0.1 同值——既有矩阵 net=accrued−0.4 语义逐行不变）。
		qty, eb, lb := 1000.0, 101.0, 100.0
		p.QtyUSD, p.EntryBasisBps, p.LastBasisBps = qty, &eb, &lb
	}
	// D-410 S5：est_fee 开仓即记账（该有）——全行给；NULL 拒毕业由 d410 测试覆盖。
	fee := 0.5
	p.EstRoundTripFeeUSD = &fee
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
	pair, err := ResolveGraduationCanonicals("BTC", "1279967", "okx", 1, entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pair.MtCanonical != "BTCUSD" || pair.CexCanonical != "BTCUSDT" || pair.MtVenueKey != "1279967:mt5" {
		t.Errorf("resolve: %+v", pair)
	}
	// 规范化命中：map 侧 XAUUSD 形 vs universe 侧 XAU
	pair, err = ResolveGraduationCanonicals("XAU", "1314615", "okx", 0, entries, nil)
	if err != nil || pair.MtCanonical != "XAUUSD" || pair.MtVenueKey != "1314615:mt4" {
		t.Errorf("normalize resolve: %+v err=%v", pair, err)
	}
	// 映射缺失 fail-closed
	if _, err := ResolveGraduationCanonicals("SOL", "1279967", "okx", 1, entries, nil); err == nil {
		t.Error("缺失映射应报错（fail-closed 不臆造）")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

// TestD468_Graduate_ActiveConvergenceMatrix D-468 S2 对抗矩阵（先红后绿）：
// active 行收敛账=entry/last basis 现算（不读 convergence_usd 列——该列仅退出时写，
// active 读列恒 NULL=原缺账闸误杀）；basis 缺=真缺账如实拒；exited 列语义不动；
// 盈利闸不放松（carry-rate-is-not-expectancy）。
func TestD468_Graduate_ActiveConvergenceMatrix(t *testing.T) {
	now := time.Now()
	colNeg := -999.0
	basis := func(p pgstore.PaperSpotCarry) pgstore.PaperSpotCarry {
		qty, eb, lb := 1000.0, 101.0, 100.0 // dir=−1 → conv=−1×(100−101)×1000/1e4=+0.1
		p.QtyUSD, p.EntryBasisBps, p.LastBasisBps = qty, &eb, &lb
		return p
	}
	cases := []struct {
		name string
		row  pgstore.PaperSpotCarry
		want bool
	}{
		{"active+列nil+basis可算→收（不读列）", basis(gradPaper(21, "active", 6, 0, 10, 0, "")), true},
		{"active+列误值−999+basis可算→收（列被忽略）", func() pgstore.PaperSpotCarry {
			p := basis(gradPaper(22, "active", 6, 0, 10, 0, ""))
			p.ConvergenceUSD = &colNeg
			return p
		}(), true},
		{"active+entry basis nil→拒（真缺账）", func() pgstore.PaperSpotCarry {
			p := gradPaper(23, "active", 6, 0, 10, 0, "")
			p.EntryBasisBps = nil
			return p
		}(), false},
		{"active+last basis nil→拒（真缺账）", func() pgstore.PaperSpotCarry {
			p := gradPaper(24, "active", 6, 0, 10, 0, "")
			p.LastBasisBps = nil
			return p
		}(), false},
		{"active+basis可算但netAllIn≤0→拒（盈利闸不动）", func() pgstore.PaperSpotCarry {
			p := basis(gradPaper(25, "active", 6, 0, 10, 0, ""))
			eb, lb := 100.0, 200.0 // conv=−1×(200−100)×1000/1e4=−10 → net=10−0.5−10=−0.5
			p.EntryBasisBps, p.LastBasisBps = &eb, &lb
			return p
		}(), false},
		{"exited+列nil→拒（原语义不动）", func() pgstore.PaperSpotCarry {
			p := gradPaper(26, "exited", 7, 1, 10, 0, "normalized")
			p.ConvergenceUSD = nil
			return p
		}(), false},
	}
	for _, tc := range cases {
		fs := &fakeGradStore{rows: []pgstore.PaperSpotCarry{tc.row}, existing: map[int64]bool{}}
		n, err := StepCarryGraduation(context.Background(), fs, now, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if (n == 1) != tc.want {
			t.Errorf("%s: newCount=%d want=%v", tc.name, n, tc.want)
		}
		if tc.want && len(fs.inserted) == 1 {
			// 收的两行：建议行 NetAllInUSD 透出=effective 现算值 9.6（accrued−fee+conv=10−0.5+0.1），
			// 列值（含 −999 误值）不透出——钉死 StepCarryGraduation 与判据同源。
			if fs.inserted[0].NetAllInUSD == nil || math.Abs(*fs.inserted[0].NetAllInUSD-9.6) > 1e-9 {
				t.Errorf("%s: NetAllInUSD want 9.6（effective 现算），got %v", tc.name, fs.inserted[0].NetAllInUSD)
			}
		}
	}
}

// TestD468_Graduate_SentinelLog D-468 S3：每轮一行 scanned/qualified/new 拒绝遥测——
// 零产出从「空表无声」变「每轮一行实数」（对照 funding regime 闸拒绝日志先例）。
func TestD468_Graduate_SentinelLog(t *testing.T) {
	old := log.Writer()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	noBasis := gradPaper(33, "active", 6, 0, 10, 0, "")
	noBasis.EntryBasisBps, noBasis.LastBasisBps = nil, nil // 真缺账行
	fs := &fakeGradStore{rows: []pgstore.PaperSpotCarry{
		gradPaper(31, "active", 6, 0, 10, 0, ""),
		gradPaper(32, "active", 7, 0, 5, 0, ""),
		noBasis,
	}, existing: map[int64]bool{}}
	n, err := StepCarryGraduation(context.Background(), fs, time.Now(), nil)
	if err != nil || n != 2 {
		t.Fatalf("newCount=%d err=%v, want 2/nil", n, err)
	}
	if !strings.Contains(buf.String(), "[graduation] scanned=3 qualified=2 new=2") {
		t.Fatalf("哨兵行缺失或数值不符: %q", buf.String())
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestD474b_PendingStale A4：pending 建议指向已衰减行 → 每轮扫描自动翻 stale +聚合 warn 一条；
// 仍合格行不动；exited 行（快照留档）不误动。
func TestD474b_PendingStale(t *testing.T) {
	now := time.Now()
	degraded := gradPaper(1, "active", 9, 0, 10, 3, "") // low_streak=3 → 不再 qualified
	fs := &fakeGradStore{
		rows:     []pgstore.PaperSpotCarry{degraded},
		existing: map[int64]bool{1: true}, // 已有建议→不重复插
		grads: []pgstore.CarryGraduation{
			{ID: 11, PaperID: 1, Status: "pending"},
			{ID: 12, PaperID: 1, Status: "approved"}, // 已裁决不动
			{ID: 13, PaperID: 99, Status: "pending"}, // 行读失败不动作
		},
	}
	if _, err := StepCarryGraduation(context.Background(), fs, now, nil); err != nil {
		t.Fatalf("step: %v", err)
	}
	if fs.decisions[11] != "stale" {
		t.Fatalf("pending+衰减 → stale，got %q", fs.decisions[11])
	}
	if _, ok := fs.decisions[12]; ok {
		t.Fatal("approved 行不得被 stale 触碰")
	}
	if _, ok := fs.decisions[13]; ok {
		t.Fatal("行读失败不误过期（下轮重试）")
	}
	// 聚合 warn 至多一条。
	warns := 0
	for _, a := range fs.alerts {
		if strings.HasPrefix(a, "warn|") {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("warn 聚合应恰 1 条，got %d: %v", warns, fs.alerts)
	}
	// 合格行不过期：低息清零后重跑不再 stale 新行。
	fs.decisions = map[int64]string{}
	fs.rows[0].LowStreak = 0
	fs.grads = []pgstore.CarryGraduation{{ID: 14, PaperID: 1, Status: "pending"}}
	if _, err := StepCarryGraduation(context.Background(), fs, now, nil); err != nil {
		t.Fatalf("step2: %v", err)
	}
	if _, ok := fs.decisions[14]; ok {
		t.Fatal("合格行不得 stale")
	}
}
