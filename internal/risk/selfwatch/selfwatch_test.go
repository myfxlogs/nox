// selfwatch_test.go —— D-033 锚：日报文案快照（全✅/部分❌）、窗起点缺省
// fail-closed、逐日净额差分、投递三通道、NextDailyBJ 定点。
// R1 锚：Stage 1 组合证据（全结构 0 active → 不就绪）、就绪推送仅 ❌→✅ 一次。
package selfwatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/research/scan"
	"nox/internal/store/pgstore"
)

type fakeStore struct {
	cfg             map[string]string
	incidents       int
	blockerID       int64
	net30d          float64
	mig             int
	venueCaps       int
	canonCaps       int
	allowReal       int
	allowPairs      []pgstore.XAssetRealCombo
	factTs          map[string]time.Time
	factCnt         map[string]int
	macroMax        *time.Time
	makerOpen       int
	makerInc        int
	dlRows          int                      // D-042 R1 ③：24h data_lost 退出行数
	dlPairs         int                      // D-042 R1 ③：当前冷却中对数
	paperSplit      pgstore.PaperActiveSplit // D-050 ②：纸面在仓两轨分列
	bbRejects       int                      // D-053 ④：24h 禁区拒开次数
	activeRows      []pgstore.PaperSpotCarry // D-061 ①：active 纸面行注入
	sessionStartMin int                      // D-061 ②：周一开盘分钟注入
	sessionMissing  bool                     // D-061 ②：会话表缺形态
	macroEvents     []pgstore.MacroEvent     // D-061 ②：禁区事件注入
	liveOpen        int                      // D-102：真金在仓数注入
	liveOpenErr     error                    // D-102：在仓计数读败形态
	facts           []fact.Fact              // D-104：InsertFacts 收集
	alerts          []string
	dedupCalls      []string
	upserts         map[string]string
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
func (f *fakeStore) XAssetAllowRealPairs(context.Context) ([]pgstore.XAssetRealCombo, error) {
	return f.allowPairs, nil
}

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
func (f *fakeStore) PaperDataLostStats(context.Context, time.Time) (int, int, error) {
	return f.dlRows, f.dlPairs, nil
}
func (f *fakeStore) PaperActiveStats(context.Context) (pgstore.PaperActiveSplit, error) {
	return f.paperSplit, nil
}
func (f *fakeStore) CountMacroBlackoutRejects(context.Context, time.Time) (int, error) {
	return f.bbRejects, nil
}
func (f *fakeStore) ListActivePaperSpotCarry(context.Context) ([]pgstore.PaperSpotCarry, error) {
	return f.activeRows, nil
}
func (f *fakeStore) MacroHighEventsIn(context.Context, time.Time, time.Time, []string) ([]pgstore.MacroEvent, error) {
	return f.macroEvents, nil
}
func (f *fakeStore) MinWeekdaySessionStartMin(context.Context, string, int) (int, bool, error) {
	if f.sessionMissing {
		return 0, false, nil
	}
	return f.sessionStartMin, true, nil
}
func (f *fakeStore) CountLiveOpenPositions(context.Context) (int, error) {
	return f.liveOpen, f.liveOpenErr
}
func (f *fakeStore) InsertFacts(_ context.Context, fs []fact.Fact) error {
	f.facts = append(f.facts, fs...)
	return nil
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

// stage1Pair 白名单实盘行（XAUUSD→规范化 XAU；MT 买腿→多 CFD 空 perp）。
var stage1Pair = pgstore.XAssetRealCombo{
	VenueA: "3001157", VenueB: "okx", Canonical: "XAUUSD", Structure: "long_cfd_short_perp",
}

func goodEval() *scan.PaperWindowEval {
	return &scan.PaperWindowEval{
		ActiveN: 3, ExitedN: 1, NetActive: 2.5, NetExited: 0.5,
		WindowNet: 3.0, NotionalUSD: 3000,
		ActiveByDay: []scan.PaperDayBucket{{Day: "10-10", Rows: 3, Sum: 2.5}},
		ExitedByDay: []scan.PaperDayBucket{{Day: "10-09", Rows: 1, Sum: 0.5, Reasons: []string{"normalized"}}},
		Combos: []scan.PaperComboStat{ // R1：Stage 1 组合行窗内累计 6.5d/accrued>0
			{VenueA: "3001157", VenueB: "okx", Canonical: "XAU", Structure: "long_cfd_short_perp",
				Rows: 3, ActiveDays: 6.5, AccruedUSD: 3.0},
		},
	}
}

func greenStore() *fakeStore {
	return &fakeStore{
		cfg: map[string]string{
			cfgPaperWindowStart:        "2026-10-03T01:59Z",
			"NOX_RISK_INCIDENT_ACK_ID": "507",
			"NOX_MAKER_TIMEOUT":        "20s",
		},
		factTs:   map[string]time.Time{"funding": tNow.Add(-30 * time.Minute)},
		factCnt:  map[string]int{"trade_session": 978},
		macroMax: ptrT(tNow.Add(-10 * time.Hour)),
		mig:      173, venueCaps: 5, canonCaps: 1, allowReal: 1,
		allowPairs: []pgstore.XAssetRealCombo{stage1Pair},
		makerOpen:  25, net30d: 100,
		// 条件制判据注入——白名单组合 active 行（存活 49h 且计息）+周一 00:00
		// 开盘（tNow 周日回退上周一，elapsed 144h≥2h）+零禁区事件。
		activeRows: []pgstore.PaperSpotCarry{{
			VenueA: "3001157", VenueB: "okx", Symbol: "XAU", Structure: "long_cfd_short_perp",
			Status: "active", EntryTs: tNow.Add(-49 * time.Hour), AccruedUSD: 3.2,
		}},
		sessionStartMin: 0,
		paperSplit:      pgstore.PaperActiveSplit{CexRows: 8, CexSyms: 5, CfdRows: 12, CfdSyms: 7},
	}
}

func ptrT(t time.Time) *time.Time { return &t }

func TestD033_DailyReportAllGreen(t *testing.T) {
	rep, err := BuildDaily(context.Background(), testDeps(greenStore(), goodEval(), tNow))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"【NOX 自盯日报】", "起点 2026-10-03T01:59Z", "窗内净额 $3.0000",
		"本窗结构（多 CFD 空 perp）在仓 3 行", "窗内退出 1 行",
		"计息缺数据 0 行", "计息停滞 0 行", "逐日净额", "全部纸面在仓：CFD 12 行·7 品种｜CEX 8 行·5 品种",
		"✅ 目标组合纸面行存活", "✅ 开仓时点", "✅ 窗内净额非负", "✅ 期间零事故仓",
		"✅ 息差数据在线", "✅ 纸面计息正常", "✅ 数据库版本 ≥172", "✅ 风控总闸在位",
		"✅ 仓位上限已定", "✅ 白名单已定", "待决策：Stage 1 可启",
		"Stage 2", "挂单模式", "XAU 3001157×okx：49h / $3.20",
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
		"❌ 期间零事故仓（2 仓）", "❌ 仓位上限已定", "❌ 纸面计息正常",
		"计息停滞 1 行", "计息缺数据 1 行",
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
	for _, want := range []string{"warn|", "停滞 2 行", "突增 0→3", "休市周表", "息差数据超龄"} {
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
	st.cfg["NOX_MACRO_ENABLED"] = "true"
	st.macroMax = ptrT(tNow.Add(-72 * time.Hour)) // 落库停滞 72h > 48h 阈
	d := testDeps(st, goodEval(), tNow)
	CheckAnomalies(context.Background(), d)
	if !strings.Contains(strings.Join(st.dedupCalls, "\n"), "财历拉取停滞") {
		t.Fatalf("missing macro warn: %v", st.dedupCalls)
	}
}

// TestD042_AnomalyDataLost paper 双 CEX 轨 data_lost 异常项（D-042 R1 ③）：
// 24h 行数 >50 → warn（消息带行数+冷却中对数）；阈值内不告警。
func TestD042_AnomalyDataLost(t *testing.T) {
	st := greenStore()
	st.dlRows, st.dlPairs = 51, 1
	CheckAnomalies(context.Background(), testDeps(st, goodEval(), tNow))
	joined := strings.Join(st.dedupCalls, "\n")
	if !strings.Contains(joined, "数据断供退出 24h 51 行") || !strings.Contains(joined, "冷却中 1 对") {
		t.Fatalf("missing data_lost warn:\n%s", joined)
	}
	st2 := greenStore()
	st2.dlRows, st2.dlPairs = 10, 0
	CheckAnomalies(context.Background(), testDeps(st2, goodEval(), tNow))
	if strings.Contains(strings.Join(st2.dedupCalls, "\n"), "数据断供") {
		t.Fatal("阈值内不应告警")
	}
}

func TestD033_NextDailyBJ(t *testing.T) {
	// 07:59 BJ → 同日 08:00；08:01 BJ → 次日 08:00；D-056③：日报定点 08:30。
	cases := []struct {
		in, want string
		h, m     int
	}{
		{"2026-10-10T07:59:00+08:00", "2026-10-10T08:00:00+08:00", 8, 0},
		{"2026-10-10T08:00:00+08:00", "2026-10-11T08:00:00+08:00", 8, 0},
		{"2026-10-10T23:59:00Z", "2026-10-11T08:00:00+08:00", 8, 0},
		{"2026-10-10T08:00:00+08:00", "2026-10-10T08:30:00+08:00", 8, 30},
		{"2026-10-10T08:30:00+08:00", "2026-10-11T08:30:00+08:00", 8, 30},
	}
	for _, c := range cases {
		in, _ := time.Parse(time.RFC3339, c.in)
		want, _ := time.Parse(time.RFC3339, c.want)
		if got := NextDailyBJ(in, c.h, c.m); !got.Equal(want) {
			t.Fatalf("NextDailyBJ(%s,%d,%d) = %s, want %s", c.in, c.h, c.m, got, c.want)
		}
	}
}

// TestD059_AnalystROProbe analyst 只读链路探针（D-059 ②/R2 ③）：
// keyword= 形态与 URL 形态（scheme://user:pass@，pgx parse 错误透 DSN 原文）错误均
// 打码进告警（告警文本零密码）；未接线→零告警。
func TestD059_AnalystROProbe(t *testing.T) {
	st := greenStore()
	d := testDeps(st, goodEval(), tNow)
	d.ProbeAnalystRO = func(context.Context) error {
		return fmt.Errorf("failed to connect to host=127.0.0.1 user=arb_ro database=arb password=secret123")
	}
	CheckAnomalies(context.Background(), d)
	joined := strings.Join(st.dedupCalls, "\n")
	if !strings.Contains(joined, "analyst 只读链路断") {
		t.Fatalf("missing probe warn:\n%s", joined)
	}
	if strings.Contains(joined, "secret123") || !strings.Contains(joined, "password=***") {
		t.Fatalf("keyword 形态应打码:\n%s", joined)
	}
	// R2 ③：URL 形态含密码错误（库内 DSN 烂→pgx parse 回显完整 DSN 原文）→ 零密码。
	st2 := greenStore()
	d2 := testDeps(st2, goodEval(), tNow)
	d2.ProbeAnalystRO = func(context.Context) error {
		return fmt.Errorf(`failed to parse config for URL "postgresql://arb_ro:sup3rs3cret@127.0.0.1:5433/arb": invalid`)
	}
	CheckAnomalies(context.Background(), d2)
	joined2 := strings.Join(st2.dedupCalls, "\n")
	if !strings.Contains(joined2, "analyst 只读链路断") {
		t.Fatalf("URL 形态应告警:\n%s", joined2)
	}
	if strings.Contains(joined2, "sup3rs3cret") {
		t.Fatalf("URL 形态密码泄漏进告警:\n%s", joined2)
	}
	if !strings.Contains(joined2, "arb_ro:***@") {
		t.Fatalf("URL 凭据段应打码:\n%s", joined2)
	}
	// 未接线 → 无告警。
	st3 := greenStore()
	CheckAnomalies(context.Background(), testDeps(st3, goodEval(), tNow))
	if strings.Contains(strings.Join(st3.dedupCalls, "\n"), "只读链路") {
		t.Fatal("未接线不应告警")
	}
}
