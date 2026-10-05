// d048_probe_test.go —— D-048①④③：探针提案契约/观测面/平仓报告锚。
// 全路径 fake（store+LLM Run seam），无真实 LLM/下单。
package explorer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"arb/internal/analyst"

	explorerv1 "arb/internal/gen/arb/explorer/v1"
	"arb/internal/store/pgstore"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// goodProbeBatch 含一合法探针提案 + 一缺字段坏行（契约缺字段拒入库锚）。
var goodProbeBatch = "摘要。\n```explorer-proposals\n" +
	`{"narrative":"n","proposals":[],"probe_proposals":[` +
	`{"purpose":"验证 UKOIL swap 实收","falsify":"24h |swap| > 0.01 USD","venue_key":"3001:mt5",` +
	`"canonical":"ETHUSDT","side":"BUY","qty":0.5,"notional_usd":300,"expected_cost_usd":1.2,` +
	`"auto_close_at":"2999-01-01T00:00:00Z","basis":"term_dispersion"},` +
	`{"purpose":"缺 falsify 的坏行","venue_key":"3001:mt5","canonical":"ETHUSDT","side":"BUY",` +
	`"qty":0.5,"notional_usd":300,"expected_cost_usd":1.2,"auto_close_at":"2999-01-01T00:00:00Z","basis":"x"}` +
	`]}` + "\n```\n"

// TestD048_ProbeContractFields 契约缺任一字段拒（逐一断言拒因非空）。
func TestD048_ProbeContractFields(t *testing.T) {
	known := map[string]bool{"ETHUSDT": true}
	now := time.Now()
	b, _, ok := parseBatch(goodProbeBatch) // D-078：签名携截断计数（此处批未超限恒 0）
	if !ok || len(b.GetProbeProposals()) != 2 {
		t.Fatalf("batch: ok=%v probes=%d", ok, len(b.GetProbeProposals()))
	}
	good, bad := b.GetProbeProposals()[0], b.GetProbeProposals()[1]
	if c, why := validateProbeContract(good, known, 1000, true, now); c == nil || why != "" {
		t.Fatalf("good contract rejected: %s", why)
	}
	if c, why := validateProbeContract(bad, known, 1000, true, now); c != nil || why == "" {
		t.Fatalf("missing falsify accepted (why=%q)", why)
	}
	// 逐字段破坏——每处必须拒且给因。
	mutate := func(src *explorerv1.ProbeProposal, fn func(*explorerv1.ProbeProposal)) *explorerv1.ProbeProposal {
		p := proto.Clone(src).(*explorerv1.ProbeProposal)
		fn(p)
		return p
	}
	f64 := func(v float64) *float64 { return &v }
	cases := map[string]*explorerv1.ProbeProposal{
		"purpose":     mutate(good, func(p *explorerv1.ProbeProposal) { p.Purpose = "" }),
		"falsify无数字":  mutate(good, func(p *explorerv1.ProbeProposal) { p.Falsify = "看 swap 收不收" }),
		"venue_key":   mutate(good, func(p *explorerv1.ProbeProposal) { p.VenueKey = "" }),
		"canonical集外": mutate(good, func(p *explorerv1.ProbeProposal) { p.Canonical = "UNKNOWN" }),
		"side":        mutate(good, func(p *explorerv1.ProbeProposal) { p.Side = "LONG" }),
		"qty":         mutate(good, func(p *explorerv1.ProbeProposal) { p.Qty = f64(0) }),
		"qty缺":        mutate(good, func(p *explorerv1.ProbeProposal) { p.Qty = nil }),
		"notional零":   mutate(good, func(p *explorerv1.ProbeProposal) { p.NotionalUsd = f64(0) }),
		"notional超":   mutate(good, func(p *explorerv1.ProbeProposal) { p.NotionalUsd = f64(2000) }),
		"cost缺":       mutate(good, func(p *explorerv1.ProbeProposal) { p.ExpectedCostUsd = nil }),
		"close过去":     mutate(good, func(p *explorerv1.ProbeProposal) { p.AutoCloseAt = timestamppb.New(now.Add(-time.Hour)) }),
		"basis":       mutate(good, func(p *explorerv1.ProbeProposal) { p.Basis = "" }),
	}
	for name, p := range cases {
		if c, why := validateProbeContract(p, known, 1000, true, now); c != nil || why == "" {
			t.Errorf("%s: 未拒（why=%q）", name, why)
		}
	}
	// 上限不可读 → 全拒（fail-closed）。
	if c, _ := validateProbeContract(good, known, 0, false, now); c != nil {
		t.Error("cap unreadable 仍放行——应 fail-closed")
	}
}

// TestD048_ProbeProposalFlow 端到端：合法行落库+待审批告警；坏行拒+log 拒因。
func TestD048_ProbeProposalFlow(t *testing.T) {
	fs := newFakeStore()
	dir := t.TempDir()
	deps := Deps{Store: fs, Client: analyst.NewClient("http://x", "k", "m"),
		Run: fakeRun(goodProbeBatch), Now: func() time.Time {
			return time.Date(2026, 9, 27, 6, 47, 0, 0, BJTime)
		}, InboxPath: dir + "/inbox.md", ResearchDir: dir + "/res"}
	res, err := RunDaily(context.Background(), deps)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	if res.ProbeInserted != 1 || len(fs.probeRows) != 1 {
		t.Fatalf("probe inserted=%d rows=%d", res.ProbeInserted, len(fs.probeRows))
	}
	row := fs.probeRows[0]
	if row.Status != "proposed" || row.Contract.VenueKey != "3001:mt5" ||
		row.Contract.NotionalUSD != 300 || row.Contract.Falsify == "" {
		t.Fatalf("row=%+v", row)
	}
	var found bool
	for _, a := range fs.alertRows {
		if strings.Contains(a, "待审批探针") && strings.Contains(a, "warn") {
			found = true
		}
	}
	if !found {
		t.Fatalf("待审批探针告警缺：%v", fs.alertRows)
	}
	if res.Drop != 1 { // 缺 falsify 坏行拒
		t.Fatalf("drop=%d want 1", res.Drop)
	}
	// 同日重跑：dedupe 命中不再插不再推。
	res2, err := RunDaily(context.Background(), deps)
	if err != nil || res2.ProbeInserted != 0 || len(fs.probeRows) != 1 {
		t.Fatalf("rerun: %+v rows=%d err=%v", res2, len(fs.probeRows), err)
	}
}

// TestD048_RunObservability ④：LLM 调用失败/解析失败→warn 告警不吞。
func TestD048_RunObservability(t *testing.T) {
	fs := newFakeStore()
	dir := t.TempDir()
	mk := func(run Runner) Deps {
		return Deps{Store: fs, Client: analyst.NewClient("http://x", "k", "m"), Run: run,
			Now:       func() time.Time { return time.Date(2026, 9, 27, 6, 47, 0, 0, BJTime) },
			InboxPath: dir + "/inbox.md", ResearchDir: dir + "/res"}
	}
	// LLM 调用失败 → err + warn。
	fs.alertRows = nil
	badRun := func(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor, _ []analyst.ToolSpec,
		_ string, _ []analyst.Message, _ analyst.LoopCallbacks) (analyst.LoopResult, error) {
		return analyst.LoopResult{}, errors.New("conn refused")
	}
	if _, err := RunDaily(context.Background(), mk(badRun)); err == nil {
		t.Fatal("LLM err 未上抛")
	}
	if !hasWarn(fs.alertRows, "LLM 调用失败") {
		t.Fatalf("LLM 失败未告警: %v", fs.alertRows)
	}
	// 解析失败 → warn（无块/截断）。
	fs.alertRows = nil
	if _, err := RunDaily(context.Background(), mk(fakeRun("无 fenced 块的输出"))); err != nil {
		t.Fatalf("parse fail err: %v", err)
	}
	if !hasWarn(fs.alertRows, "解析失败") {
		t.Fatalf("解析失败未告警: %v", fs.alertRows)
	}
}

func hasWarn(rows []string, sub string) bool {
	for _, a := range rows {
		if strings.HasPrefix(a, "warn") && strings.Contains(a, sub) {
			return true
		}
	}
	return false
}

// TestD048_ProbeReportOnce ③：CLOSED+probe_expired → 报告一次（认领幂等）；
// 重复触发零再生成；文件含模板段。
func TestD048_ProbeReportOnce(t *testing.T) {
	fs := newFakeStore()
	dir := t.TempDir()
	now := time.Now()
	openT := now.Add(-2 * time.Hour)
	closeT := now.Add(-time.Minute)
	ref := "D-048"
	notional := 300.0
	pnl := -1.23
	fs.probePos = &pgstore.ProbeRow{
		Position: pgstore.Position{ID: 42, Type: "PROBE", Status: "CLOSED",
			Note: "验证免息", DecisionRef: &ref, NotionalUSD: &notional,
			RealizedPnl: &pnl, OpenedAt: &openT, ClosedAt: &closeT, ExitKind: pgstore.ExitKindProbeExpired},
		VenueKey: "3001:mt5", Symbol: "UKOILm", Side: "SELL", LegQty: 0.1}
	openPx, closePx, fee := 68.0, 68.1, 0.05
	fs.legs = []pgstore.PositionLeg{
		{PositionID: 42, LegNo: 1, Phase: "OPEN", Venue: "3001:mt5", Side: "SELL", Qty: 0.1,
			PriceActual: &openPx, OrderID: "t-1", Status: "FILLED", FeeUSD: &fee, Ts: openT},
		{PositionID: 42, LegNo: 1, Phase: "CLOSE", Venue: "3001:mt5", Side: "BUY", Qty: 0.1,
			PriceActual: &closePx, OrderID: "t-2", Status: "FILLED", Ts: closeT},
	}
	fs.cashflows = []pgstore.Cashflow{
		{PositionID: 42, Kind: "commission", Venue: "3001", AmountUSD: -0.05, Ts: openT},
	}
	pid := int64(42)
	fs.probeRows = append(fs.probeRows, pgstore.ProbeProposal{
		ID: 7, Status: "closed", PositionID: &pid,
		Contract: pgstore.ProbeContract{Purpose: "验证 UKOIL 免息", Falsify: "|swap| > 0.01 USD",
			VenueKey: "3001:mt5", Canonical: "UKOIL", Side: "SELL", Qty: 0.1,
			NotionalUSD: 300, ExpectedCostUSD: 0.1, Basis: "D-045"},
	})
	llmOut := "取证：\n```probe-report\n" +
		`{"conclusion":"A","analysis":"两腿成交价差 -1.23 USD，现金流仅 commission -0.05，无 swap 行","recommendation":"可按免息口径评估"}` +
		"\n```\n"
	calls := 0
	run := func(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor, _ []analyst.ToolSpec,
		_ string, _ []analyst.Message, cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
		calls++
		cb.OnDelta(llmOut)
		return analyst.LoopResult{}, nil
	}
	deps := Deps{Store: fs, Client: analyst.NewClient("http://x", "k", "m"), Run: run,
		ResearchDir: dir, Now: func() time.Time { return now }}

	path, gen, err := GenerateProbeReport(context.Background(), deps, 42)
	if err != nil || !gen || calls != 1 {
		t.Fatalf("gen=%v calls=%d err=%v", gen, calls, err)
	}
	if !strings.Contains(path, "probe-D-048-") || !strings.HasSuffix(path, ".md") {
		t.Fatalf("path=%s", path)
	}
	body, _ := os.ReadFile(path)
	for _, sec := range []string{"## 假设", "## 成交记录", "## 现金流", "## 调整记录", "## 结论 A", "## 建议", "验证 UKOIL 免息"} {
		if !strings.Contains(string(body), sec) {
			t.Fatalf("报告缺段 %q:\n%s", sec, body)
		}
	}
	if !hasWarn(fs.alertRows, "探针报告") {
		t.Fatalf("报告推送告警缺: %v", fs.alertRows)
	}
	// 重复触发：认领已在 → 零 LLM 调用零文件。
	_, gen2, err := GenerateProbeReport(context.Background(), deps, 42)
	if err != nil || gen2 || calls != 1 {
		t.Fatalf("replay: gen=%v calls=%d err=%v", gen2, calls, err)
	}
	// 无仓行 → facts err 不生成（claim 占位+warn 告警路径）。
	if _, gen3, err := GenerateProbeReport(context.Background(), deps, 99); err == nil || gen3 {
		t.Errorf("无仓行仍生成报告 gen=%v err=%v", gen3, err)
	}
	if !hasWarn(fs.alertRows, "报告生成失败") {
		t.Fatalf("报告失败未告警: %v", fs.alertRows)
	}
}
