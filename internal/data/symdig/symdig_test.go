// symdig_test.go D-464 T2/T3：四分类+mispaired 检出+LLM 提案只进收件箱。
package symdig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"arb/internal/data/fact"
	"arb/internal/platform/analyst"
	"arb/internal/store/pgstore"
)

// fakeStore 罐装 facts/映射/别名/universe 键 + findings/alert 记录。
type fakeStore struct {
	facts    []fact.Fact
	mappings []pgstore.SymbolMapping
	aliases  map[string]string
	uni      map[string]bool
	prevKeys map[string]bool

	mu       sync.Mutex
	inserted []pgstore.SymdigFinding
	alerts   []string
}

func (f *fakeStore) LatestFactsByKind(ctx context.Context, kind string) ([]fact.Fact, error) {
	return f.LatestFacts(ctx, kind, "", "")
}

func (f *fakeStore) LatestFacts(_ context.Context, kind, _, _ string) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, x := range f.facts {
		if x.Kind == kind {
			out = append(out, x)
		}
	}
	return out, nil
}
func (f *fakeStore) ListSymbolMappings(_ context.Context, _ string) ([]pgstore.SymbolMapping, error) {
	return f.mappings, nil
}
func (f *fakeStore) ListSymbolAliases(context.Context) (map[string]string, error) {
	return f.aliases, nil
}
func (f *fakeStore) ListCarryUniversePairs(context.Context) (map[string]bool, error) {
	return f.uni, nil
}
func (f *fakeStore) LatestSymdigKeys(context.Context) (map[string]bool, error) {
	return f.prevKeys, nil
}
func (f *fakeStore) InsertSymdigFindings(_ context.Context, _ time.Time, rows []pgstore.SymdigFinding) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserted = append(f.inserted, rows...)
	return nil
}
func (f *fakeStore) InsertAlert(_ context.Context, _ *int64, level, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = append(f.alerts, level+":"+msg)
	return nil
}

func klassOf(rows []pgstore.SymdigFinding, venue, sym string) (string, string) {
	for _, r := range rows {
		if r.Venue == venue && r.Symbol == sym {
			return r.Klass, r.Reason
		}
	}
	return "", ""
}

// T2：四分类各一 + mispaired（同 venue 同 canonical 双 is_primary）。
func TestSymdigClassify(t *testing.T) {
	now := time.Now().UTC() // D-512 T7 归档连带修：Round 内 runTS=Now().UTC()，测试时钟快照
	// 必须同为 UTC——本地 +08:00 在 23:5x 起评时 UTC 已跨日→报告文件名错位（KB test-seed-wallclock-leak 同族：墙钟依赖）。
	st := &fakeStore{
		facts: []fact.Fact{
			{Kind: fact.KindTradeMode, Venue: "v1", Symbol: "XAUUSDm", Value: fact.TradeModeFull, Ts: now},
			{Kind: fact.KindTradeMode, Venue: "v1", Symbol: "GOLD", Value: fact.TradeModeFull, Ts: now},
			{Kind: fact.KindTradeMode, Venue: "v1", Symbol: "SHIBx", Value: fact.TradeModeDisabled, Ts: now},
			{Kind: fact.KindTradeMode, Venue: "v1", Symbol: "MYSTERY", Value: fact.TradeModeFull, Ts: now},
			{Kind: fact.KindFunding, Venue: "gate", Symbol: "XAUUSDT", Value: 1.0, Ts: now},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "v1", BrokerSymbol: "XAUUSDm", Canonical: "XAU", IsPrimary: true},
			{Venue: "v1", BrokerSymbol: "XAUUSD", Canonical: "XAU", IsPrimary: true}, // 双 primary→mispaired
		},
		uni:      map[string]bool{"v1|XAU": true},
		prevKeys: map[string]bool{},
	}
	dir := t.TempDir()
	deps := Deps{Store: st, Now: func() time.Time { return now },
		ReportDir: filepath.Join(dir, "报告")}
	sum, err := Round(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if k, _ := klassOf(st.inserted, "v1", "XAUUSDm"); k != KlassPaired {
		t.Errorf("XAUUSDm klass=%s want paired", k)
	}
	if k, _ := klassOf(st.inserted, "v1", "GOLD"); k != KlassUnmappedUnknown {
		t.Errorf("GOLD klass=%s want unmapped_unknown", k)
	}
	if k, _ := klassOf(st.inserted, "v1", "SHIBx"); k != KlassDisabled {
		t.Errorf("SHIBx klass=%s want disabled", k)
	}
	if k, _ := klassOf(st.inserted, "v1", "MYSTERY"); k != KlassUnmappedUnknown {
		t.Errorf("MYSTERY klass=%s want unmapped_unknown", k)
	}
	if k, r := klassOf(st.inserted, "v1", "XAU"); k != KlassMispaired || r != "multi_primary" {
		t.Errorf("mispaired klass=%s reason=%s", k, r)
	}
	if len(sum.NewUnpaired) == 0 {
		t.Error("首轮新增 unpaired 应非空")
	}
	if len(st.alerts) != 1 || !strings.Contains(st.alerts[0], "info:") {
		t.Errorf("alerts=%v", st.alerts)
	}
	// 报告件落地
	data, err := os.ReadFile(filepath.Join(dir, "报告", "symdig-"+now.Format("2006-01-02")+".md"))
	if err != nil || !strings.Contains(string(data), "unpaired") {
		t.Fatalf("report err=%v", err)
	}
	// 第二轮同 findings → 无新增不告警
	prev := map[string]bool{}
	for _, r := range st.inserted {
		prev[r.Venue+"|"+r.Symbol+"|"+r.Klass] = true
	}
	st2 := &fakeStore{facts: st.facts, mappings: st.mappings, uni: st.uni, prevKeys: prev}
	sum2, err := Round(context.Background(), Deps{Store: st2, Now: deps.Now})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum2.NewUnpaired) != 0 || len(st2.alerts) != 0 {
		t.Errorf("第二轮应零新增零告警: new=%v alerts=%v", sum2.NewUnpaired, st2.alerts)
	}
}

// T2b：unpaired 归因——canonical 有 CEX funding→filtered；无→no_cex_counterpart。
func TestSymdigUnpairedReason(t *testing.T) {
	st := &fakeStore{
		facts: []fact.Fact{
			{Kind: fact.KindTradeMode, Venue: "v1", Symbol: "A", Value: fact.TradeModeFull, Ts: time.Now()},
			{Kind: fact.KindTradeMode, Venue: "v1", Symbol: "B", Value: fact.TradeModeFull, Ts: time.Now()},
			{Kind: fact.KindFunding, Venue: "okx", Symbol: "AAAUSDT", Value: 1.0, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "v1", BrokerSymbol: "A", Canonical: "AAA"},
			{Venue: "v1", BrokerSymbol: "B", Canonical: "BBB"},
		},
		uni: map[string]bool{},
	}
	sum, err := classify(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if _, r := klassOf(sum.Rows, "v1", "A"); r != "filtered" {
		t.Errorf("A reason=%s want filtered（有 CEX funding 无 universe 行）", r)
	}
	if _, r := klassOf(sum.Rows, "v1", "B"); r != "no_cex_counterpart" {
		t.Errorf("B reason=%s want no_cex_counterpart", r)
	}
}

// T3：LLM 提案只进收件箱——fake runner 回放 symdig-proposals 块；断言 inbox 行含
// kind=symdig_alias 且 alias/map 表零写入（fake store 无别名写面=编译期即证）。
func TestSymdigLLMInbox(t *testing.T) {
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox.md")
	os.WriteFile(inbox, []byte("| date | source | title | reason | action |\n|---|---|---|---|---|\n"), 0o644)
	st := &fakeStore{
		facts: []fact.Fact{
			{Kind: fact.KindTradeMode, Venue: "v1", Symbol: "GOLD", Value: fact.TradeModeFull, Ts: time.Now()},
		},
		uni: map[string]bool{},
	}
	run := func(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor,
		_ []analyst.ToolSpec, _ string, msgs []analyst.Message,
		cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
		if !strings.Contains(string(msgs[0].Content), "GOLD") {
			t.Errorf("prompt 应含 findings 行")
		}
		cb.OnDelta("```symdig-proposals\n[{\"symbol\":\"GOLD\",\"venue\":\"v1\",\"proposed_canonical\":\"XAU\",\"evidence\":\"gold=xau semantic\",\"confidence\":0.9}]\n```")
		return analyst.LoopResult{}, nil
	}
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	_, err := Round(context.Background(), Deps{
		Store: st, Client: &analyst.Client{}, Run: run, Now: func() time.Time { return now },
		InboxPath: inbox, ReportDir: filepath.Join(dir, "报告"),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(inbox)
	if !strings.Contains(string(data), "symdig_alias") || !strings.Contains(string(data), "GOLD→XAU") {
		t.Fatalf("inbox missing proposal: %s", data)
	}
}

// T3b：未闭合 proposals 块 EOF 容忍 + warn alert。
func TestSymdigLLMUnclosed(t *testing.T) {
	props, malformed := parseProposals("前言\n```symdig-proposals\n[{\"symbol\":\"GOLD\",\"venue\":\"v1\",\"proposed_canonical\":\"XAU\",\"evidence\":\"e\",\"confidence\":0.5}")
	if len(props) != 1 || props[0].ProposedCanonical != "XAU" || !malformed {
		t.Fatalf("props=%v malformed=%v", props, malformed)
	}
}

// conf=0.00 不入箱 + 同 symbol 当日 pending 去重。
func TestSymdigInboxDedup(t *testing.T) {
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox.md")
	props := []proposal{
		{Symbol: "GOLD", Venue: "v1", ProposedCanonical: "XAU", Evidence: "e", Confidence: 0.9},
		{Symbol: "ZERO", Venue: "v1", ProposedCanonical: "Z", Evidence: "e", Confidence: 0.0},
	}
	if err := appendInbox(inbox, "2026-09-27", "symdig_alias", props); err != nil {
		t.Fatal(err)
	}
	// 当日重投同 symbol → 抑制
	if err := appendInbox(inbox, "2026-09-27", "symdig_alias", props); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(inbox)
	if strings.Count(string(data), "GOLD→XAU") != 1 {
		t.Fatalf("GOLD 应只入箱一次: %s", data)
	}
	if strings.Contains(string(data), "ZERO→") {
		t.Fatalf("conf=0 提案不应入箱: %s", data)
	}
}
