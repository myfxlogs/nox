// selfwatch_test.go —— D-033 锚：日报文案快照（全✅/部分❌）、窗起点缺省
// fail-closed、逐日净额差分、投递三通道、NextDailyBJ 定点。
package selfwatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arb/internal/scan"
	"arb/internal/store/pgstore"
)

type fakeStore struct {
	cfg        map[string]string
	incidents  int
	blockerID  int64
	net30d     float64
	mig        int
	venueCaps  int
	canonCaps  int
	allowReal  int
	factTs     map[string]time.Time
	factCnt    map[string]int
	macroMax   *time.Time
	makerOpen  int
	makerInc   int
	alerts     []string
	dedupCalls []string
	upserts    map[string]string
}

func (f *fakeStore) AppConfigString(_ context.Context, k string) (string, bool, error) {
	if v, ok := f.upserts[k]; ok {
		return v, true, nil
	}
	v, ok := f.cfg[k]
	return v, ok, nil
}
func (f *fakeStore) AppConfigInt64(_ context.Context, k string) (int64, bool, error) {
	v, ok := f.cfg[k]
	if !ok {
		return 0, false, nil
	}
	var n int64
	fmt.Sscanf(v, "%d", &n)
	return n, true, nil
}
func (f *fakeStore) UpsertAppConfig(_ context.Context, k, v, _ string) error {
	if f.upserts == nil {
		f.upserts = map[string]string{}
	}
	f.upserts[k] = v
	return nil
}
func (f *fakeStore) PaperWindowCohort(context.Context, string, time.Time) ([]pgstore.PaperSpotCarry, error) {
	return nil, nil
}
func (f *fakeStore) IncidentCountSince(context.Context, time.Time) (int, error) {
	return f.incidents, nil
}
func (f *fakeStore) IncidentBlockerID(_ context.Context, _ int64) (int64, error) {
	return f.blockerID, nil
}
func (f *fakeStore) NetRealizedSince(context.Context, time.Time, bool) (float64, error) {
	return f.net30d, nil
}
func (f *fakeStore) SchemaMigrationMax(context.Context) (int, error) { return f.mig, nil }
func (f *fakeStore) ExposureCapCounts(context.Context) (int, int, error) {
	return f.venueCaps, f.canonCaps, nil
}
func (f *fakeStore) XAssetAllowRealCount(context.Context) (int, error) { return f.allowReal, nil }
func (f *fakeStore) LatestFactTs(_ context.Context, kind string) (time.Time, bool, error) {
	ts, ok := f.factTs[kind]
	return ts, ok, nil
}
func (f *fakeStore) CountFactsSince(_ context.Context, kind string, _ time.Time) (int, error) {
	return f.factCnt[kind], nil
}
func (f *fakeStore) MacroEventMaxCreated(context.Context) (time.Time, bool, error) {
	if f.macroMax == nil {
		return time.Time{}, false, nil
	}
	return *f.macroMax, true, nil
}
func (f *fakeStore) MakerLegStatsSince(context.Context, time.Time) (int, int, error) {
	return f.makerOpen, f.makerInc, nil
}
func (f *fakeStore) InsertAlert(_ context.Context, _ *int64, level, msg string) error {
	f.alerts = append(f.alerts, level+"|"+msg)
	return nil
}
func (f *fakeStore) InsertAlertDedup(_ context.Context, level, msg string, w time.Duration) error {
	f.dedupCalls = append(f.dedupCalls, level+"|"+msg+"|"+w.String())
	return nil
}

func testDeps(st *fakeStore, ev *scan.PaperWindowEval, now time.Time) Deps {
	return Deps{
		Store: st,
		EvalPaper: func(context.Context, []pgstore.PaperSpotCarry, time.Time, time.Time) (*scan.PaperWindowEval, error) {
			return ev, nil
		},
		Weekly:      func(context.Context, time.Time) (string, error) { return "weekly-body", nil },
		KillActive:  func(context.Context) (bool, error) { return false, nil },
		CapitalBase: 10000,
		Now:         func() time.Time { return now },
		ReportDir:   "",
	}
}

var tNow = time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC) // 08:00 BJ 10-11（窗龄 7.9d ≥7d 阈）

func goodEval() *scan.PaperWindowEval {
	return &scan.PaperWindowEval{
		ActiveN: 3, ExitedN: 1, NetActive: 2.5, NetExited: 0.5,
		WindowNet: 3.0, NotionalUSD: 3000,
		ActiveByDay: []scan.PaperDayBucket{{Day: "10-10", Rows: 3, Sum: 2.5}},
		ExitedByDay: []scan.PaperDayBucket{{Day: "10-09", Rows: 1, Sum: 0.5, Reasons: []string{"normalized"}}},
	}
}

func greenStore() *fakeStore {
	return &fakeStore{
		cfg: map[string]string{
			cfgPaperWindowStart:        "2026-10-03T01:59Z",
			"ARB_RISK_INCIDENT_ACK_ID": "507",
			"ARB_MAKER_TIMEOUT":        "20s",
		},
		factTs:   map[string]time.Time{"funding": tNow.Add(-30 * time.Minute)},
		factCnt:  map[string]int{"trade_session": 978},
		macroMax: ptrT(tNow.Add(-10 * time.Hour)),
		mig:      173, venueCaps: 5, canonCaps: 1, allowReal: 1,
		makerOpen: 25, net30d: 100,
	}
}

func ptrT(t time.Time) *time.Time { return &t }

func TestD033_DailyReportAllGreen(t *testing.T) {
	rep, err := BuildDaily(context.Background(), testDeps(greenStore(), goodEval(), tNow))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"【ARB 自盯日报】", "起点 2026-10-03T01:59Z", "窗内净额 $3.0000", "在仓 3 行", "窗内退出 1 行",
		"fail-closed 0 行", "accrue 停滞 0 行", "逐日净额",
		"✅ paper 窗 ≥7 天", "✅ 期间零事故仓", "✅ D-027", "✅ D-028", "✅ D-032",
		"✅ cap 已定", "✅ 白名单已定", "待决策：Stage 1 可启",
		"Stage 2", "maker 分级",
	} {
		if !strings.Contains(rep.Body, want) {
			t.Fatalf("body missing %q:\n%s", want, rep.Body)
		}
	}
	if !rep.Stage1OK {
		t.Fatal("Stage1OK should be true")
	}
	// Stage 2：net30d=100 → 年化 100/30*365/10000=12.17% ≥8 → ✅；maker 25≥20 → ✅
	if !rep.Stage2Table[0].OK || !rep.Stage2Table[2].OK {
		t.Fatalf("stage2 rows: %+v", rep.Stage2Table)
	}
}

func TestD033_DailyReportPartialFail(t *testing.T) {
	st := greenStore()
	st.incidents = 2 // 期间事故
	st.canonCaps = 0 // canonical cap 缺
	ev := goodEval()
	ev.Stalled = 1
	ev.FailClosed = 1
	rep, err := BuildDaily(context.Background(), testDeps(st, ev, tNow))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"❌ 期间零事故仓（2 仓）", "❌ cap 已定", "❌ D-028",
		"accrue 停滞 1 行", "fail-closed 1 行",
	} {
		if !strings.Contains(rep.Body, want) {
			t.Fatalf("body missing %q:\n%s", want, rep.Body)
		}
	}
	if strings.Contains(rep.Body, "待决策：Stage 1 可启") || rep.Stage1OK {
		t.Fatalf("should not be stage1-ready:\n%s", rep.Body)
	}
}

func TestD033_WindowStartMissing(t *testing.T) {
	st := greenStore()
	delete(st.cfg, cfgPaperWindowStart)
	os.Unsetenv(cfgPaperWindowStart)
	rep, err := BuildDaily(context.Background(), testDeps(st, nil, tNow))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Body, "未配置") {
		t.Fatalf("should report missing window start:\n%s", rep.Body)
	}
}

func TestD033_DailyNetHistDelta(t *testing.T) {
	st := greenStore()
	d := testDeps(st, goodEval(), tNow)
	if _, err := BuildDaily(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildDaily(context.Background(),
		testDeps(st, &scan.PaperWindowEval{WindowNet: 3.5}, tNow.Add(24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	hist := st.upserts[cfgPaperNetHist]
	if !strings.Contains(hist, "2026-10-11") || !strings.Contains(hist, "2026-10-12") {
		t.Fatalf("hist missing days: %s", hist)
	}
	// 第二日报告应含 +0.5000 增量行
	rep2, err := BuildDaily(context.Background(),
		testDeps(st, &scan.PaperWindowEval{WindowNet: 4.0}, tNow.Add(48*time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep2.Body, "+0.5000") {
		t.Fatalf("delta line missing:\n%s", rep2.Body)
	}
}

func TestD033_DeliverThreeChannels(t *testing.T) {
	st := greenStore()
	dir := t.TempDir()
	d := testDeps(st, nil, tNow)
	d.ReportDir = dir
	d.deliver(context.Background(), "daily", "info", "报告体")
	path := filepath.Join(dir, "selfwatch-daily-2026-10-11.md")
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "报告体" {
		t.Fatalf("report file: %v %q", err, b)
	}
	if len(st.alerts) != 1 || !strings.HasPrefix(st.alerts[0], "info|") {
		t.Fatalf("alerts: %v", st.alerts)
	}
}

func TestD033_AnomalyWarns(t *testing.T) {
	st := greenStore()
	st.factTs = map[string]time.Time{"funding": tNow.Add(-10 * time.Hour)} // funding 超龄
	st.factCnt = map[string]int{"trade_session": 0}                        // 周表缺席
	ev := goodEval()
	ev.Stalled = 2
	ev.FailClosed = 3
	d := testDeps(st, ev, tNow)
	CheckAnomalies(context.Background(), d)
	joined := strings.Join(st.dedupCalls, "\n")
	for _, want := range []string{"warn|", "停滞 2 行", "突增 0→3", "trade_session", "funding facts 超龄"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing warn %q in:\n%s", want, joined)
		}
	}
	for _, c := range st.dedupCalls {
		if !strings.HasSuffix(c, "|1h0m0s") {
			t.Fatalf("dedup window: %s", c)
		}
	}
	// fail-closed 基数已持久化（环比下轮用）
	if st.upserts[cfgFailClosedPrev] != "3" {
		t.Fatalf("prev not saved: %v", st.upserts)
	}
	// macro 关闭态（未置 true）→ 不产生财历告警
	if strings.Contains(joined, "财历") {
		t.Fatal("macro disabled should skip calendar check")
	}
}

func TestD033_AnomalyMacroStale(t *testing.T) {
	st := greenStore()
	st.cfg["ARB_MACRO_ENABLED"] = "true"
	st.macroMax = ptrT(tNow.Add(-72 * time.Hour)) // 落库停滞 72h > 48h 阈
	d := testDeps(st, goodEval(), tNow)
	CheckAnomalies(context.Background(), d)
	if !strings.Contains(strings.Join(st.dedupCalls, "\n"), "财历拉取停滞") {
		t.Fatalf("missing macro warn: %v", st.dedupCalls)
	}
}

func TestD033_NextDailyBJ(t *testing.T) {
	// 07:59 BJ → 同日 08:00；08:01 BJ → 次日 08:00
	cases := []struct{ in, want string }{
		{"2026-10-10T07:59:00+08:00", "2026-10-10T08:00:00+08:00"},
		{"2026-10-10T08:00:00+08:00", "2026-10-11T08:00:00+08:00"},
		{"2026-10-10T23:59:00Z", "2026-10-11T08:00:00+08:00"},
	}
	for _, c := range cases {
		in, _ := time.Parse(time.RFC3339, c.in)
		want, _ := time.Parse(time.RFC3339, c.want)
		if got := NextDailyBJ(in, 8, 0); !got.Equal(want) {
			t.Fatalf("NextDailyBJ(%s) = %s, want %s", c.in, got, c.want)
		}
	}
}
