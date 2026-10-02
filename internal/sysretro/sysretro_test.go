package sysretro

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"arb/internal/analyst"
	"arb/internal/store/pgstore"
)

// fakeRetroStore 注入罐装统计 + 记录 alert。
type fakeRetroStore struct {
	daily      *pgstore.RetroDaily
	weekly     *pgstore.RetroWeekly
	depth      []pgstore.DepthFact
	mu         sync.Mutex
	alerts     []string
	dailySince time.Time
	dailyUntil time.Time
}

func (f *fakeRetroStore) RetroDailyStats(_ context.Context, since, until time.Time) (*pgstore.RetroDaily, error) {
	f.dailySince, f.dailyUntil = since, until
	return f.daily, nil
}
func (f *fakeRetroStore) RetroWeeklyStats(context.Context, time.Time, time.Time) (*pgstore.RetroWeekly, error) {
	return f.weekly, nil
}
func (f *fakeRetroStore) DepthFacts(context.Context, time.Time, time.Time) ([]pgstore.DepthFact, error) {
	return f.depth, nil
}
func (f *fakeRetroStore) InsertAlert(_ context.Context, _ *int64, level, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = append(f.alerts, level+":"+msg)
	return nil
}
func (f *fakeRetroStore) alertRows() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.alerts...)
}

// fakeRun 断言 prompt 含统计数字，回放罐装 AI 文本。
func fakeRun(t *testing.T, wantSubstr, reply string) Runner {
	return func(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor,
		_ []analyst.ToolSpec, _ string, msgs []analyst.Message,
		cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
		if wantSubstr != "" && !strings.Contains(string(msgs[0].Content), wantSubstr) {
			t.Errorf("prompt missing %q", wantSubstr)
		}
		cb.OnDelta(reply)
		return analyst.LoopResult{}, nil
	}
}

func testDeps(t *testing.T, st Store, run Runner) Deps {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "STATE.md"), []byte(`## 交接负载
- 阻塞/待决策: ws-api 待决策
||| **D-457 系统周期复盘报告** | 🔄 施工中 | x |`), 0o644)
	os.WriteFile(filepath.Join(dir, "inbox.md"), []byte("| 条目 | 源 |\n|---|---|\n| 旧行 | x |\n"), 0o644)
	return Deps{
		Store: st, Client: &analyst.Client{}, Run: run,
		Now:        func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC) },
		ReportDir:  filepath.Join(dir, "报告"),
		InboxPath:  filepath.Join(dir, "inbox.md"),
		StatePath:  filepath.Join(dir, "STATE.md"),
		RepoDir:    dir, // 非 git 仓→降级分支
		EntriesDir: filepath.Join(dir, "条目"),
	}
}

// T1：日报五节契约顺序+数字嵌入+AI 小结位置+交接面+alert 通知。
func TestGenerateDaily(t *testing.T) {
	st := &fakeRetroStore{daily: &pgstore.RetroDaily{
		OpenPos:     []pgstore.RetroOpenPos{{ID: 502, Type: "XASSET_CARRY", Venue: "gate", OpenedAt: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), CarryUSD: 1.03}},
		OpenedToday: 1, ClosedToday: 0,
		FundingActualUSD: 1.0313, FundingEstUSD: 1.15,
		GraduationsToday: 2, // D-468 S4：毕业建议窗口计数渲染
		EvalDist:         map[string]int{"hold": 100, "fail_closed": 2},
		AlertDist:        map[string]int{"warn": 1},
		AlertTop3:        []string{"x"},
	}}
	deps := testDeps(t, st, fakeRun(t, "funding_actual_today: $1.0313", "今日平稳，funding 实收达标。"))
	path, err := GenerateDaily(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	txt := string(data)
	order := []string{"## 今日盘面", "## 今日决策轨", "## 今日事件", "## 今日小结", "## 交接面"}
	last := -1
	for _, h := range order {
		i := strings.Index(txt, h)
		if i < 0 || i < last {
			t.Fatalf("section %q missing/out-of-order", h)
		}
		last = i
	}
	for _, want := range []string{"pos502", "hold=100", "今日平稳", "D-457 系统周期复盘报告", "ws-api 待决策", "inbox_backlog_rows: 1", "git log 不可用", "北京时间全天", "graduations_today: 2"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("report missing %q", want)
		}
	}
	if !strings.HasSuffix(path, "sysretro-daily-2026-09-23.md") {
		t.Fatalf("path=%s", path)
	}
	// R1（D-510）：窗口=昨日北京时间全天。Now=UTC 9-24 12:00（BJ 20:00）→「昨日 BJ 历日」=9-23
	// → 窗=[BJ 9-23 00:00, BJ 9-24 00:00)=[UTC 9-22 16:00, 9-23 16:00)。
	if !st.dailySince.Equal(time.Date(2026, 9, 22, 16, 0, 0, 0, time.UTC)) ||
		!st.dailyUntil.Equal(time.Date(2026, 9, 23, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("window=[%v,%v)", st.dailySince, st.dailyUntil)
	}
	if len(st.alertRows()) != 1 || !strings.Contains(st.alertRows()[0], "info:") {
		t.Fatalf("alerts=%v", st.alertRows())
	}
}

// T2：周报五节+建议进收件箱+未闭合 fenced 容忍+warn alert。
func TestGenerateWeekly(t *testing.T) {
	st := &fakeRetroStore{weekly: &pgstore.RetroWeekly{
		Groups: []pgstore.RetroClosedGroup{{Type: "XASSET_CARRY", N: 2, Wins: 1, Losses: 1, PnlUSD: 0.5, EstBps: 10, RealBps: 8}},
		TopWin: &pgstore.RetroClosedCase{ID: 500, Type: "XASSET_CARRY", PnlUSD: 1.2},
		Strategies: []pgstore.RetroStrategyRow{
			{Strategy: "PERP_FUNDING", Trades: 10, WinRate: 0.8, TotalPnl: 3.3},
			{Strategy: "DEAD", Disabled: true, Reason: "losers"},
		},
		PaperSpot:   map[string]int{"low_funding": 3},
		PaperIntra:  map[string]int{},
		PaperTrades: 0,
	}}
	// AI 输出：五节 + 未闭合 suggestions 块（EOF 截断）。
	ai := `## 交易成败归因
本周两仓一盈一亏。
## 纸面交易复盘
壳表零行是缺口。
## 策略生死状
一策略已淘汰。
## 踩坑与经验
KB 有新增。
## 改进建议
补 paper_trades 采集 | 壳表没产数 | 立观测票
` + "```sysretro-suggestions\n补 paper_trades 采集 | 壳表没产数 | 立观测票"
	deps := testDeps(t, st, fakeRun(t, "top_win", ai))
	path, err := GenerateWeekly(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	txt := string(mustRead(t, path))
	for _, h := range []string{"## 交易成败归因", "## 纸面交易复盘", "## 策略生死状", "## 踩坑与经验", "## 改进建议"} {
		if !strings.Contains(txt, h) {
			t.Fatalf("missing %q", h)
		}
	}
	for _, want := range []string{"XASSET_CARRY n=2", "pos500", "DISABLED(losers)", "壳表 0 行=观测面缺口自曝", "补 paper_trades 采集"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("report missing %q", want)
		}
	}
	// 未闭合 fenced 块建议仍进收件箱 + warn alert。
	inbox := string(mustRead(t, deps.InboxPath))
	if !strings.Contains(inbox, "补 paper_trades 采集") || !strings.Contains(inbox, "sysretro-weekly") {
		t.Fatalf("inbox=%s", inbox)
	}
	var hasWarn, hasInfo bool
	for _, a := range st.alertRows() {
		if strings.HasPrefix(a, "warn:") && strings.Contains(a, "格式异常") {
			hasWarn = true
		}
		if strings.HasPrefix(a, "info:") {
			hasInfo = true
		}
	}
	if !hasWarn || !hasInfo {
		t.Fatalf("alerts=%v", st.alertRows())
	}
}

// T3（D-510 重写）：NextDelay 两档 BJ 锚点（daily 00:30 BJ / weekly 周一 00:30 BJ）。
// 含 SSOT 验收锚：16:30 UTC 恰界用例（BJ 次日 00:30=UTC 16:30）+BJ 历日窗口对拍
// （BJ 周一 00:30 触发的日报窗=UTC 周日 16:00→周一 16:00）。
func TestDelayUntilRetro(t *testing.T) {
	bj := BJTime
	// 2026-09-24 是周四。UTC 12:00 = BJ 20:00 → 距 BJ 周五 00:30 = 4.5h。
	thu := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	if d := NextDelay(thu, "daily"); d != 4*time.Hour+30*time.Minute {
		t.Fatalf("daily delay=%v want 4h30m（BJ 20:00→次日 00:30）", d)
	}
	// BJ 恰界：UTC 16:20 = BJ 00:20（次日历日内早于 00:30）→ 10 分钟后触发。
	early := time.Date(2026, 9, 24, 16, 20, 0, 0, time.UTC)
	if d := NextDelay(early, "daily"); d != 10*time.Minute {
		t.Fatalf("daily early delay=%v want 10m（BJ 00:20→00:30）", d)
	}
	// SSOT 锚：UTC 16:30 恰界 = BJ 9-25 00:30 整点 → 恰过取下一周期=BJ 9-26 00:30。
	atEdge := time.Date(2026, 9, 24, 16, 30, 0, 0, time.UTC)
	d := NextDelay(atEdge, "daily")
	landBJ := atEdge.Add(d).In(bj)
	if landBJ.Day() != 26 || landBJ.Hour() != 0 || landBJ.Minute() != 30 || d != 24*time.Hour {
		t.Fatalf("16:30 UTC 恰界应恰 24h 落 BJ 9-26 00:30，got d=%v land=%v", d, landBJ)
	}
	// weekly：周四 UTC 12:00（BJ 20:00）→ 下周一 BJ 00:30 = UTC 周日 16:30。
	if d := NextDelay(thu, "weekly"); d <= 0 {
		t.Fatalf("weekly delay 非未来: %v", d)
	} else if land := thu.Add(d).In(bj); land.Weekday() != time.Monday || land.Hour() != 0 || land.Minute() != 30 {
		t.Fatalf("weekly 应落 BJ 周一 00:30，got %v", land)
	}
	// weekly 恰界：BJ 周一 00:30 整点（UTC 周日 16:30）→ 下周一。
	monBJ := time.Date(2026, 9, 28, 0, 30, 0, 0, bj) // BJ 周一 00:30
	if d := NextDelay(monBJ, "weekly"); monBJ.Add(d).In(bj).Day() != 5 {
		t.Fatalf("weekly at-exact 应落 10-05（下周一），got %v", monBJ.Add(d).In(bj))
	}
}

// TestD510_DailyWindow_BJCalendar SSOT 验收：BJ 历日窗口对拍——BJ 周一 00:30 触发的
// 日报窗=[BJ 周日 00:00, BJ 周一 00:00)=[UTC 周日 16:00, 周一 16:00)，与 funding 8h 结算
// 边界（UTC 16:00 起）严丝合缝装 3 个完整结算窗。
func TestD510_DailyWindow_BJCalendar(t *testing.T) {
	trigger := time.Date(2026, 9, 28, 0, 30, 0, 0, BJTime) // BJ 周一 00:30
	nowBJ := trigger.In(BJTime)
	dayEnd := time.Date(nowBJ.Year(), nowBJ.Month(), nowBJ.Day(), 0, 0, 0, 0, BJTime)
	dayStart := dayEnd.AddDate(0, 0, -1)
	// 「昨日 BJ 历日」=9-27 → 窗=[UTC 9-26 16:00, 9-27 16:00)（funding 结算边界起）。
	if got := dayStart.UTC().Format("2006-01-02 15:04"); got != "2026-09-26 16:00" {
		t.Fatalf("窗口起点=UTC %s，want 2026-09-26 16:00（funding 结算边界）", got)
	}
	if got := dayEnd.UTC().Format("2006-01-02 15:04"); got != "2026-09-27 16:00" {
		t.Fatalf("窗口终点=UTC %s，want 2026-09-27 16:00", got)
	}
	// 3 个完整 8h 结算窗（16:00/00:00/08:00 UTC 起算）。
	windows := 0
	for ts := dayStart; ts.Before(dayEnd); ts = ts.Add(8 * time.Hour) {
		windows++
	}
	if windows != 3 {
		t.Fatalf("BJ 历日窗应装 3 个 8h 结算窗，got %d", windows)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
