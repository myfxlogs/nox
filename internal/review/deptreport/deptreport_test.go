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
	perf     *pgstore.PerfReport
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
func (f *fakeStore) ListDeptReports(_ context.Context, date time.Time) ([]pgstore.DeptReport, error) {
	var out []pgstore.DeptReport
	for _, r := range f.reports {
		if r.ReportDate.Equal(date) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeStore) UpsertDeptReport(_ context.Context, r pgstore.DeptReport) error {
	f.reports = append(f.reports, r)
	return nil
}
func (f *fakeStore) InsertAlertDedup(_ context.Context, _, msg string, _ time.Duration) error {
	f.alerts = append(f.alerts, msg)
	return nil
}
func (f *fakeStore) PerfSinceBaseline(context.Context, time.Time) (*pgstore.PerfReport, error) {
	return f.perf, nil
}

type fakeNotifier struct{ sent []string }

func (f *fakeNotifier) Enabled() bool { return true }
func (f *fakeNotifier) SendText(_ context.Context, _, text string) (string, error) {
	f.sent = append(f.sent, text)
	return "mid", nil
}

// contractBody 契约合规正文样例（八段序+末行齐——fakeRunner 缺省返回，防
// 既有锚被契约重试径污染）。
const contractBody = `①NAV 与闲置：NAV 10000。
②在仓与 carry 实收：0 仓。
③待决提案：0。
④请求箱：0。
⑤策略态变：无。
⑥事故与硬线：无。
⑦流健康：正常。
⑧昨日待办→今日状态：无基可参。
明日关注：无。`

// fakeRunner 回 text 并记录 system prompt 与 user 输入。
// bodies 队列>0 时逐次弹取（契约重试径锚）；空→contractBody。
type fakeRunner struct {
	mu      sync.Mutex
	prompts []string
	inputs  []string
	fail    error
	bodies  []string
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
	body := contractBody
	if len(f.bodies) > 0 {
		body = f.bodies[0]
		f.bodies = f.bodies[1:]
	}
	cb.OnDelta(body)
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
		pools:  []pgstore.ExplorerIdlePool{{Venue: "binance", Equity: 9000, Balance: 4000, Currency: "USD", Idle: true}},
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
		pools: []pgstore.ExplorerIdlePool{{Venue: "binance", Equity: 9000, Balance: 4000, Currency: "USD", Idle: true}},
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

// TestD249_TodoLoop D-249②：昨日 company 行原文入输入⑧段——跨日待办对拍面；
// 缺日=空段注明不误引。
func TestD249_TodoLoop(t *testing.T) {
	fs := &fakeStore{rd: &pgstore.RetroDaily{},
		pools: []pgstore.ExplorerIdlePool{{Venue: "binance", Equity: 9000, Currency: "USD"}}}
	yesterday := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	fs.reports = append(fs.reports, pgstore.DeptReport{
		Dept: DeptCompany, ReportDate: yesterday, Body: "昨日正文……明日关注：盯提案#7 与请求箱#3"})
	fr := &fakeRunner{}
	date := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if err := Generate(context.Background(), testDeps(fs, fr, nil), date); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !strings.Contains(fr.inputs[0], "【⑧ 昨日公司日报原文】") ||
		!strings.Contains(fr.inputs[0], "明日关注：盯提案#7") {
		t.Fatalf("昨日行应入⑧段: %s", fr.inputs[0][len(fr.inputs[0])-300:])
	}
	if !strings.Contains(fr.prompts[0], "昨日待办") {
		t.Fatal("系统提示应含昨日待办对照段")
	}
	// 缺日=空段注明
	fs2 := &fakeStore{rd: &pgstore.RetroDaily{}}
	fr2 := &fakeRunner{}
	if err := Generate(context.Background(), testDeps(fs2, fr2, nil), date); err != nil {
		t.Fatalf("Generate2: %v", err)
	}
	if !strings.Contains(fr2.inputs[0], "昨日无 company 行") {
		t.Fatal("缺日应空段注明")
	}
}

// TestD249_ContractMiss 契约校验器：缺段/乱序/合规三态钉死（schema 只管结构）。
func TestD249_ContractMiss(t *testing.T) {
	if miss := contractMiss(contractBody); len(miss) != 0 {
		t.Fatalf("合规样例误判: %v", miss)
	}
	// 缺 ③④ → 两违
	if miss := contractMiss("NAV 1\n在仓无\n策略变\n事故无\n流健康\n昨日待办\n明日关注"); len(miss) != 2 {
		t.Fatalf("缺段 miss=%v want 2", miss)
	}
	// 乱序（④请求箱先于③提案）→ 违
	if miss := contractMiss("NAV 1\n在仓无\n请求箱\n提案\n策略\n事故\n流健康\n待办\n明日关注"); len(miss) == 0 {
		t.Fatal("乱序未检出")
	}
	// 缺末行
	if miss := contractMiss("NAV\n在仓\n提案\n请求箱\n策略\n事故\n流健康\n待办"); len(miss) != 1 || miss[0] != "末行明日关注" {
		t.Fatalf("末行缺 miss=%v", miss)
	}
}

// TestD249_ContractRetry 首班违约→补正重生成一次→合规落库（两调用+补正注记入输入）。
func TestD249_ContractRetry(t *testing.T) {
	fs := &fakeStore{rd: &pgstore.RetroDaily{}, pools: []pgstore.ExplorerIdlePool{}}
	fr := &fakeRunner{bodies: []string{"只有 NAV 一段。", contractBody}}
	d := testDeps(fs, fr, nil)
	if err := Generate(context.Background(), d, time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(fr.inputs) != 2 {
		t.Fatalf("调用数=%d want 2（补正一次）", len(fr.inputs))
	}
	if !strings.Contains(fr.inputs[1], "契约补正") || !strings.Contains(fr.inputs[1], "在仓") {
		t.Fatalf("补正注记缺: %q", fr.inputs[1][:min(200, len(fr.inputs[1]))])
	}
	if len(fs.reports) != 1 || !strings.Contains(fs.reports[0].Body, "在仓") || strings.Contains(fs.reports[0].Body, "契约缺段") {
		t.Fatalf("落库正文异常: %+v", fs.reports[0])
	}
}

// TestD249_ContractFailOpen 二次仍违→fail-open 落库+「契约缺段」标记（零丢报）。
func TestD249_ContractFailOpen(t *testing.T) {
	fs := &fakeStore{rd: &pgstore.RetroDaily{}, pools: []pgstore.ExplorerIdlePool{}}
	fr := &fakeRunner{bodies: []string{"只有 NAV。", "仍只有 NAV。"}}
	d := testDeps(fs, fr, nil)
	if err := Generate(context.Background(), d, time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Generate 应 fail-open 落库: %v", err)
	}
	if len(fs.reports) != 1 || !strings.Contains(fs.reports[0].Body, "契约缺段") {
		t.Fatalf("缺段标记未落: %+v", fs.reports)
	}
	// 视窗 C 件：仍缺段须带 gather 原料段回填（②在仓段原文在正文中），marker 仍标。
	body := fs.reports[0].Body
	if !strings.Contains(body, "【契约补段·原料 ⑥事故与硬线】") ||
		!strings.Contains(body, "告警分布") {
		t.Fatalf("⑥ 原料段未回填: %q", body)
	}
}

// TestContractBackfill 原料段回填纯锚：圈码段拼回+末行无原料段只留 marker+禁编造。
func TestContractBackfill(t *testing.T) {
	input := "【必备指标】\n- NAV=1\n\n【① NAV 与闲置资金】\n- 池1\n\n【② 在仓与 carry 实收】\n- 仓2\n\n【⑥ 事故与硬线】\n- 告警分布：（无）\n- 硬线：未触碰\n\n【⑦ 流健康】\n- 流=0\n"
	out := contractBackfill("正文NAV 提案 请求箱 策略 流健康 待办 明日关注", input,
		[]string{"②在仓与 carry", "⑥事故与硬线", "末行明日关注"})
	if !strings.Contains(out, "【契约补段·原料 ②在仓与 carry】\n【② 在仓与 carry 实收】\n- 仓2") {
		t.Fatalf("② 段未逐字拼回: %q", out)
	}
	if !strings.Contains(out, "- 硬线：未触碰") {
		t.Fatalf("⑥ 原料缺: %q", out)
	}
	if strings.Contains(out, "末行明日关注】\n") {
		t.Fatal("末行无原料段却生成了补段")
	}
	// 段边界：⑦ 头不得混入 ⑥ 回填。
	if strings.Contains(out[strings.Index(out, "硬线：未触碰"):], "流健康】\n- 流=0") {
		t.Fatal("段边界越界混入 ⑦")
	}
}
