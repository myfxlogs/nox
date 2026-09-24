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
	daily  *pgstore.RetroDaily
	weekly *pgstore.RetroWeekly
	mu     sync.Mutex
	alerts []string
}

func (f *fakeRetroStore) RetroDailyStats(context.Context, time.Time, time.Time) (*pgstore.RetroDaily, error) {
	return f.daily, nil
}
func (f *fakeRetroStore) RetroWeeklyStats(context.Context, time.Time, time.Time) (*pgstore.RetroWeekly, error) {
	return f.weekly, nil
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
		EvalDist:  map[string]int{"hold": 100, "fail_closed": 2},
		AlertDist: map[string]int{"warn": 1},
		AlertTop3: []string{"x"},
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
	for _, want := range []string{"pos502", "hold=100", "今日平稳", "D-457 系统周期复盘报告", "ws-api 待决策", "inbox_backlog_rows: 1", "git log 不可用"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("report missing %q", want)
		}
	}
	if !strings.HasSuffix(path, "sysretro-daily-2026-09-24.md") {
		t.Fatalf("path=%s", path)
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
		if strings.HasPrefix(a, "warn:") && strings.Contains(a, "未闭合") {
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

// T3：delayUntilRetro 两档对齐（daily 00:30 / weekly 周一 01:00）。
func TestDelayUntilRetro(t *testing.T) {
	// 2026-09-24 是周四。
	thu := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	if d := NextDelay(thu, "daily"); d != 12*time.Hour+30*time.Minute {
		t.Fatalf("daily delay=%v", d)
	}
	early := time.Date(2026, 9, 24, 0, 15, 0, 0, time.UTC)
	if d := NextDelay(early, "daily"); d != 15*time.Minute {
		t.Fatalf("daily early delay=%v", d)
	}
	if d := NextDelay(thu, "weekly"); time.Until(thu.Add(d).UTC()) <= 0 ||
		thu.Add(d).Weekday() != time.Monday || thu.Add(d).Hour() != 1 {
		t.Fatalf("weekly delay=%v lands %v", d, thu.Add(d))
	}
	mon := time.Date(2026, 9, 28, 1, 0, 0, 0, time.UTC) // 周一 01:00 整点 → 下周一
	if d := NextDelay(mon, "weekly"); mon.Add(d).Day() != 5 {
		t.Fatalf("weekly at-exact delay=%v lands %v", d, mon.Add(d))
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
