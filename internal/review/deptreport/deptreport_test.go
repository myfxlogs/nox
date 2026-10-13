// deptreport_test.go —— M4.0/D-246：公司日报单调用编排/money-first 输入序/
// 脱敏/业主面泄漏拦截锚测试（fake store+fake runner，无 DB）。
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
	reports  []pgstore.DeptReport
	rd       *pgstore.RetroDaily
	retroErr error
	alerts   []string
	pools    []pgstore.ExplorerIdlePool
	props    []pgstore.Proposal
	gaps     []pgstore.ConditionGap
	disabled map[string]bool
	llmUSD   float64
	llmN     int
}

func (f *fakeStore) RetroDailyStats(context.Context, time.Time, time.Time) (*pgstore.RetroDaily, error) {
	return f.rd, f.retroErr
}
func (f *fakeStore) ExplorerIdleFunds(context.Context, time.Time) ([]pgstore.ExplorerIdlePool, error) {
	return f.pools, nil
}
func (f *fakeStore) ComputeNav(_ context.Context, now time.Time) (*pgstore.NavReport, error) {
	return pgstore.SumNav(f.pools, now), nil
}
func (f *fakeStore) NetRealizedSince(context.Context, time.Time, bool) (float64, error) {
	return -100.0, nil
}
func (f *fakeStore) ListProposals(context.Context, string, int) ([]pgstore.Proposal, error) {
	return f.props, nil
}
func (f *fakeStore) ListConditionGaps(context.Context, string) ([]pgstore.ConditionGap, error) {
	return f.gaps, nil
}
func (f *fakeStore) ListDisabledStrategies(context.Context) (map[string]bool, error) {
	return f.disabled, nil
}
func (f *fakeStore) LLMCallCostSince(context.Context, time.Time) (float64, int, error) {
	return f.llmUSD, f.llmN, nil
}
func (f *fakeStore) UpsertDeptReport(_ context.Context, r pgstore.DeptReport) error {
	f.reports = append(f.reports, r)
	return nil
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
	fail    error
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
	if f.fail != nil {
		return analyst.LoopResult{}, f.fail
	}
	cb.OnDelta("公司日报正文")
	return analyst.LoopResult{InputTokens: 11, OutputTokens: 22}, nil
}

func testDeps(fs *fakeStore, fr *fakeRunner, fn *fakeNotifier) *Deps {
	d := &Deps{
		Store:          fs,
		Client:         &analyst.Client{APIKey: "k"},
		Run:            fr.run,
		CapitalBaseUSD: 10000,
		ChatID:         func(context.Context) (string, error) { return "chat", nil },
	}
	if fn != nil { // 显式判空——typed-nil Notifier 接口非 nil 会击穿 d.Notify==nil 闸
		d.Notify = fn
	}
	return d
}

// TestGenerate_CompanySingleCall M4.0：全日仅 1 次 LLM 调用→dept='company' 单行+推送。
func TestGenerate_CompanySingleCall(t *testing.T) {
	fs := &fakeStore{rd: &pgstore.RetroDaily{OpenedToday: 2, FundingActualUSD: 3.5,
		AlertDist: map[string]int{"warn": 1}, EvalDist: map[string]int{"A": 2}},
		pools:  []pgstore.ExplorerIdlePool{{Venue: "binance", Equity: 9000, Balance: 4000, Currency: "USD"}},
		llmUSD: 1.23, llmN: 7}
	fr := &fakeRunner{}
	fn := &fakeNotifier{}
	date := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if err := Generate(context.Background(), testDeps(fs, fr, fn), date); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(fr.inputs) != 1 {
		t.Fatalf("LLM 调用数=%d want 1", len(fr.inputs))
	}
	if len(fs.reports) != 1 || fs.reports[0].Dept != DeptCompany {
		t.Fatalf("reports=%+v want 1 company 行", fs.reports)
	}
	r := fs.reports[0]
	if r.PromptTokens != 11 || r.CompletionTokens != 22 {
		t.Fatalf("tokens=%d/%d want 11/22", r.PromptTokens, r.CompletionTokens)
	}
	if len(fn.sent) != 1 || !strings.Contains(fn.sent[0], "NOX 公司日报") {
		t.Fatalf("feishu push=%v", fn.sent)
	}
	if len(fs.alerts) != 1 {
		t.Fatalf("alert rows=%d", len(fs.alerts))
	}
}

// TestGather_MoneyFirst 输入段 money-first 固定序：必备指标→①NAV→…→⑦流健康；
// 名册段不存在（零「员工/在职/离职」措辞）。
func TestGather_MoneyFirst(t *testing.T) {
	fs := &fakeStore{rd: &pgstore.RetroDaily{},
		pools: []pgstore.ExplorerIdlePool{{Venue: "binance", Equity: 9000, Balance: 4000, Currency: "USD"}},
		props: []pgstore.Proposal{{ID: 7, TemplateID: "carry_x", Venue: "binance",
			CreatedAt: time.Now().Add(-72 * time.Hour)}},
		gaps: []pgstore.ConditionGap{
			{ID: 3, Kind: "capital", Subject: "USD", RequestText: "请划转", Status: "requested",
				CreatedAt: time.Now().Add(-24 * time.Hour)},
			{ID: 9, Kind: "runtime", Subject: "x", Status: "waived",
				CreatedAt: time.Now().Add(-48 * time.Hour)},
		},
		disabled: map[string]bool{"swaparb": true}, llmUSD: 1.23, llmN: 7}
	dayEnd := time.Date(2026, 10, 13, 0, 0, 0, 0, BJTime)
	fs.props[0].CreatedAt = dayEnd.Add(-72 * time.Hour)
	fs.gaps[0].CreatedAt = dayEnd.Add(-24 * time.Hour)
	d := testDeps(fs, &fakeRunner{}, nil)
	d.fill()
	input, err := gather(context.Background(), d,
		time.Date(2026, 10, 12, 0, 0, 0, 0, BJTime), dayEnd)
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	order := []string{"【必备指标】", "【① NAV 与闲置资金】", "【② 在仓与 carry 实收】",
		"【③ 待决提案（含龄）】", "【④ 请求箱】", "【⑤ 策略态变】", "【⑥ 事故与硬线】", "【⑦ 流健康】"}
	pos := -1
	for _, seg := range order {
		i := strings.Index(input, seg)
		if i < 0 || i < pos {
			t.Fatalf("段序错 %s: %.400s", seg, input)
		}
		pos = i
	}
	for _, bad := range []string{"员工", "在职", "离职", "考勤"} {
		if strings.Contains(input, bad) {
			t.Fatalf("输入含名册措辞 %q: %.400s", bad, input)
		}
	}
	// 必备指标原值：NAV=9000 闲置=4000→44.4%；提案龄 3 天；最老请求龄=24h 行。
	if !strings.Contains(input, "NAV=9000.00") || !strings.Contains(input, "44.4%") {
		t.Fatalf("NAV/闲置比指标错: %.300s", input)
	}
	if !strings.Contains(input, "3.0 天") || !strings.Contains(input, "#7 carry_x") {
		t.Fatalf("提案龄错: %.400s", input)
	}
	if !strings.Contains(input, "投资人侧") || !strings.Contains(input, "系统侧") ||
		!strings.Contains(input, "请划转") {
		t.Fatalf("请求箱分列错: %.400s", input)
	}
	if !strings.Contains(input, "LLM 成本=1.23") || !strings.Contains(input, "swaparb") {
		t.Fatalf("LLM 成本/策略态变错: %.400s", input)
	}
}

// TestGenerate_FailOpen LLM 失败返错零落库；AI 未配置当班跳过。
func TestGenerate_FailOpen(t *testing.T) {
	fs := &fakeStore{rd: &pgstore.RetroDaily{}}
	fr := &fakeRunner{fail: context.DeadlineExceeded}
	d := testDeps(fs, fr, nil)
	date := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if err := Generate(context.Background(), d, date); err == nil {
		t.Fatal("LLM 失败应返错（fail-open 由 Run 层吞）")
	}
	if len(fs.reports) != 0 {
		t.Fatalf("失败零落库: %d", len(fs.reports))
	}
	d2 := &Deps{Store: fs, Run: fr.run} // Client nil + ResolveClient nil=未配置
	if err := Generate(context.Background(), d2, date); err == nil {
		t.Fatal("AI 未配置应返当班跳过错")
	}
}

// TestScrubBody 脱敏闸：触发词「币安/冻结」洗净 + D-247③ 业主面泄漏行整行剔除。
func TestScrubBody(t *testing.T) {
	in := "NAV 正常。\nIR部建议业主面披露下周计划。\nbinance 报价冻结 100 USD。\n明日关注：提案龄。"
	got := scrubBody(in)
	if strings.Contains(got, "建议业主") || strings.Contains(got, "业主面") {
		t.Fatalf("业主面泄漏行未剔: %q", got)
	}
	if strings.Contains(got, "币安") || strings.Contains(got, "冻结") {
		t.Fatalf("触发词未脱敏: %q", got)
	}
	if !strings.Contains(got, "明日关注") || !strings.Contains(got, "NAV 正常") {
		t.Fatalf("正常行被误剔: %q", got)
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
