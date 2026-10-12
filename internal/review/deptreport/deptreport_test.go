// deptreport_test.go —— D-244：Generate 编排/财务专项/经理摘要锚测试（fake store+fake runner，无 DB）。
package deptreport

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"nox/internal/platform/analyst"
	"nox/internal/store/pgstore"
)

// fakeStore Store 面内存实现。
type fakeStore struct {
	reports   []pgstore.DeptReport
	emps      []pgstore.Employee
	rd        *pgstore.RetroDaily
	retroErr  error
	alerts    []string
	pnlErr    error
	incomeErr error
}

func (f *fakeStore) ListEmployees(context.Context) ([]pgstore.Employee, error) { return f.emps, nil }
func (f *fakeStore) RetroDailyStats(context.Context, time.Time, time.Time) (*pgstore.RetroDaily, error) {
	return f.rd, f.retroErr
}
func (f *fakeStore) FinancePnlByEnv(context.Context) ([]pgstore.FinancePnlEnv, error) {
	return []pgstore.FinancePnlEnv{{Environment: "real", RealizedPnl: 12.34, Closed: 2}}, f.pnlErr
}
func (f *fakeStore) FinanceIncomeByKind(context.Context) ([]pgstore.FinanceIncomeKind, error) {
	return []pgstore.FinanceIncomeKind{{Kind: "funding", TotalUSD: 56.78, Rows: 3}}, f.incomeErr
}
func (f *fakeStore) LatestVenueAccountSnapshots(context.Context) ([]pgstore.VenueAccountLatest, error) {
	eq := 100.0
	return []pgstore.VenueAccountLatest{{Venue: "binance", VenueType: "perp", EquityRaw: &eq, Currency: "USD"}}, nil
}
func (f *fakeStore) UpsertDeptReport(_ context.Context, r pgstore.DeptReport) error {
	f.reports = append(f.reports, r)
	return nil
}
func (f *fakeStore) ListDeptReports(context.Context, time.Time) ([]pgstore.DeptReport, error) {
	return f.reports, nil
}
func (f *fakeStore) InsertAlertDedup(_ context.Context, _, msg string, _ time.Duration) error {
	f.alerts = append(f.alerts, msg)
	return nil
}

type fakeNotifier struct{ sent []string }

func (f *fakeNotifier) Enabled() bool { return true }
func (f *fakeNotifier) SendText(_ context.Context, _, text string) (string, error) {
	f.sent = append(f.sent, text)
	return "mid", nil
}

// fakeRunner 回 text 并记录 system prompt 与 user 输入。
type fakeRunner struct {
	mu      sync.Mutex
	prompts []string
	inputs  []string
	failFor string // 含此子的 dept 输入→返错
}

func (f *fakeRunner) run(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor,
	_ []analyst.ToolSpec, system string, msgs []analyst.Message,
	cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, system)
	var user string
	_ = json.Unmarshal(msgs[0].Content, &user)
	f.inputs = append(f.inputs, user)
	f.mu.Unlock()
	if f.failFor != "" && strings.Contains(user, f.failFor) {
		return analyst.LoopResult{}, context.DeadlineExceeded
	}
	cb.OnDelta("报告正文-" + system[:8])
	return analyst.LoopResult{InputTokens: 11, OutputTokens: 22}, nil
}

func testDeps(fs *fakeStore, fr *fakeRunner, fn *fakeNotifier) *Deps {
	d := &Deps{
		Store:  fs,
		Client: &analyst.Client{APIKey: "k"},
		Run:    fr.run,
		ChatID: func(context.Context) (string, error) { return "chat", nil },
	}
	if fn != nil { // 显式判空——typed-nil Notifier 接口非 nil 会击穿 d.Notify==nil 闸
		d.Notify = fn
	}
	return d
}

// TestGenerate_AllDeptsAndManager 十一部+经理摘要全落库、token/耗时留痕、飞书扇出。
func TestGenerate_AllDeptsAndManager(t *testing.T) {
	fs := &fakeStore{rd: &pgstore.RetroDaily{OpenedToday: 2, FundingActualUSD: 3.5,
		AlertDist: map[string]int{"warn": 1}, EvalDist: map[string]int{"A": 2}}}
	fr := &fakeRunner{}
	fn := &fakeNotifier{}
	date := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if err := Generate(context.Background(), testDeps(fs, fr, fn), date); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := len(fs.reports); got != len(pgstore.EmployeeDepts)+1 {
		t.Fatalf("reports=%d want %d", got, len(pgstore.EmployeeDepts)+1)
	}
	var mgr *pgstore.DeptReport
	depts := map[string]bool{}
	for i := range fs.reports {
		r := &fs.reports[i]
		depts[r.Dept] = true
		if r.PromptTokens != 11 || r.CompletionTokens != 22 {
			t.Fatalf("dept=%s tokens=%d/%d want 11/22", r.Dept, r.PromptTokens, r.CompletionTokens)
		}
		if r.Dept == DeptManager {
			mgr = r
		}
	}
	for _, d := range pgstore.EmployeeDepts {
		if !depts[d] {
			t.Fatalf("dept %s 缺报", d)
		}
	}
	if mgr == nil {
		t.Fatal("manager 行缺")
	}
	// 经理输入=十一部报正文（manager 摘要 prompt 为第二条 system 型）。
	if len(fr.inputs) != len(pgstore.EmployeeDepts)+1 {
		t.Fatalf("llm calls=%d", len(fr.inputs))
	}
	mgrInput := fr.inputs[len(fr.inputs)-1]
	if !strings.Contains(mgrInput, "=== 账本部 ===") {
		t.Fatalf("manager 输入缺部报段: %.200s", mgrInput)
	}
	if len(fn.sent) != 1 || !strings.Contains(fn.sent[0], "NOX 公司日报") {
		t.Fatalf("feishu push=%v", fn.sent)
	}
	if len(fs.alerts) != 1 {
		t.Fatalf("alert rows=%d", len(fs.alerts))
	}
}

// TestGather_LedgerFinance 账本部输入含财务专项段；其余部不含。
func TestGather_LedgerFinance(t *testing.T) {
	fs := &fakeStore{rd: &pgstore.RetroDaily{}}
	fr := &fakeRunner{}
	d := testDeps(fs, fr, nil)
	d.fill()
	date := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if err := Generate(context.Background(), d, date); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var ledger, other string
	for i, in := range fr.inputs[:len(pgstore.EmployeeDepts)] {
		if strings.Contains(in, "部门：账本") {
			ledger = fr.inputs[i]
		} else {
			other = fr.inputs[i]
		}
	}
	if !strings.Contains(ledger, "财务专项·venue 资金池快照") || !strings.Contains(ledger, "100.00") {
		t.Fatalf("账本输入缺财务段: %.300s", ledger)
	}
	if strings.Contains(other, "财务专项") {
		t.Fatalf("非账本误含财务段: %.300s", other)
	}
}

// TestGenerate_FailOpen 单部 LLM 失败不阻塞其余与摘要；AI 未配置整班跳过。
func TestGenerate_FailOpen(t *testing.T) {
	fs := &fakeStore{rd: &pgstore.RetroDaily{}}
	fr := &fakeRunner{failFor: "部门：风控"}
	d := testDeps(fs, fr, &fakeNotifier{})
	date := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if err := Generate(context.Background(), d, date); err != nil {
		t.Fatalf("Generate fail-open 违背: %v", err)
	}
	if got := len(fs.reports); got != len(pgstore.EmployeeDepts) { // 10 部 + manager
		t.Fatalf("reports=%d want %d", got, len(pgstore.EmployeeDepts))
	}

	d2 := &Deps{Store: fs, Run: fr.run} // Client nil + ResolveClient nil=未配置
	if err := Generate(context.Background(), d2, date); err == nil {
		t.Fatal("AI 未配置应返当班跳过错")
	}
}

// TestScrub_1301 脱敏闸：触发词「币安行情/冻结」在部报输入、manager 输入、
// 落库正文三面均洗净（2026-10-13 生产实证 1301——CJK 短语硬触发+冻结金额并置软触发）。
func TestScrub_1301(t *testing.T) {
	fs := &fakeStore{rd: &pgstore.RetroDaily{}, emps: []pgstore.Employee{
		{Name: "main:binance-mkt-ws", Role: "币安行情 WS", Dept: "数据", Running: true, Enabled: true},
	}}
	fr := &fakeRunner{}
	d := testDeps(fs, fr, nil)
	d.fill()
	date := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if err := Generate(context.Background(), d, date); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for i, in := range fr.inputs {
		if strings.Contains(in, "币安") || strings.Contains(in, "冻结") {
			t.Fatalf("input[%d] 未脱敏: %.200s", i, in)
		}
	}
	if !strings.Contains(fr.inputs[0], "binance") {
		t.Fatalf("数据输入应含英文 venue 名: %.200s", fr.inputs[0])
	}
	// 存量正文含触发词 → manager 输入侧洗净。
	fs2 := &fakeStore{reports: []pgstore.DeptReport{{Dept: "研究", Body: "疑似冻结报价"}}}
	fr2 := &fakeRunner{}
	d2 := testDeps(fs2, fr2, nil)
	d2.fill()
	if err := d2.genManager(context.Background(), &analyst.Client{APIKey: "k"}, date); err != nil {
		t.Fatalf("genManager: %v", err)
	}
	if strings.Contains(fr2.inputs[0], "冻结") || !strings.Contains(fr2.inputs[0], "停滞") {
		t.Fatalf("manager 输入未脱敏: %.200s", fr2.inputs[0])
	}
}

// TestReportDate_BJ 报告日=前一 BJ 历日（UTC 钉零时）。
func TestReportDate_BJ(t *testing.T) {
	got := ReportDate(time.Date(2026, 10, 13, 7, 5, 0, 0, BJTime))
	want := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("ReportDate=%v want %v", got, want)
	}
}
