// kbreport_test.go —— D-442 S5：统计扫描/模板拼装/报告生成单测（先红后绿）。
// T1 Scan fixture 全字段断言；T2 模板拼装结构断言；T3 fake Runner 断言 prompt 含统计块+
// 输出路径模式+待归纳追加行格式；附 delayUntilNextMondayUTC 边界用例。
package kbreport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arb/internal/platform/analyst"
)

// writeEntry 写人造条目（frontmatter + 正文可选 falsifies_when 节）。
func writeEntry(t *testing.T, dir, id, kind, risk, status, firstSeen, lastVerified string, kws, scopes []string, falsifies bool) {
	t.Helper()
	body := fmt.Sprintf(`---
schema_version: 2
id: %s
kind: %s
risk: %s
status: %s
keywords: [%s]
scope: [%s]
summary: test entry %s
first_seen: %s
last_verified: %s
---

## 事实/症状

x
`, id, kind, risk, status, strings.Join(kws, ", "), strings.Join(scopes, ", "), id, firstSeen, lastVerified)
	if falsifies {
		body += "\n## 反证条件\n\n- falsifies_when: 条件失效\n"
	}
	if err := os.WriteFile(filepath.Join(dir, id+".md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newFixtureDir 5 条人造条目覆盖各桶/缺项（now 固定 2026-09-24）。
func newFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeEntry(t, dir, "e-fresh", "pitfall", "high", "verified", "2026-09-01", "2026-09-20",
		[]string{"grpc", "stream"}, []string{"internal/adapter"}, true)
	writeEntry(t, dir, "e-mid", "pitfall", "medium", "verified", "2026-06-01", "2026-08-01",
		[]string{"grpc"}, []string{"internal/adapter", "tools"}, true)
	writeEntry(t, dir, "e-old", "pattern", "low", "observed", "2026-01-01", "2026-03-01",
		[]string{"git"}, []string{"scripts"}, true)
	writeEntry(t, dir, "e-nolv", "pitfall", "high", "verified", "2026-09-10", "",
		[]string{"grpc"}, []string{"internal/adapter"}, false)
	writeEntry(t, dir, "e-nofalse", "strategy", "critical", "disputed", "2026-08-30", "2026-09-15",
		[]string{"pnl"}, []string{"internal/store"}, false)
	return dir
}

var fixtureNow = time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

// TestScanFixture T1：统计字段全对。
func TestScanFixture(t *testing.T) {
	s, err := Scan(newFixtureDir(t), fixtureNow)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if s.Total != 5 {
		t.Fatalf("total=%d want 5", s.Total)
	}
	if s.ByKind["pitfall"] != 3 || s.ByKind["pattern"] != 1 || s.ByKind["strategy"] != 1 {
		t.Errorf("by_kind=%v", s.ByKind)
	}
	if s.ByRisk["high"] != 2 || s.ByRisk["critical"] != 1 {
		t.Errorf("by_risk=%v", s.ByRisk)
	}
	if s.ByStatus["verified"] != 3 || s.ByStatus["observed"] != 1 || s.ByStatus["disputed"] != 1 {
		t.Errorf("by_status=%v", s.ByStatus)
	}
	// top_scope: internal/adapter×3 必居首
	if len(s.TopScope) == 0 || s.TopScope[0].Name != "internal/adapter" || s.TopScope[0].Count != 3 {
		t.Errorf("top_scope=%v", s.TopScope)
	}
	// top_keywords: grpc×3 居首
	if len(s.TopKeywords) == 0 || s.TopKeywords[0].Name != "grpc" || s.TopKeywords[0].Count != 3 {
		t.Errorf("top_keywords=%v", s.TopKeywords)
	}
	// stale 分桶：09-20/09-15(<30d)=2；08-01(30-90d)=1；03-01(>90d)=1；缺失=1
	if s.StaleLT30 != 2 || s.Stale30To90 != 1 || s.StaleGT90 != 1 || s.StaleMissing != 1 {
		t.Errorf("stale=%d/%d/%d/%d", s.StaleLT30, s.Stale30To90, s.StaleGT90, s.StaleMissing)
	}
	// 质量缺项：e-nolv、e-nofalse 无 falsifies_when
	if len(s.QualityGaps) != 2 {
		t.Errorf("quality_gaps=%v", s.QualityGaps)
	}
	// 近 30d 新增：e-fresh(09-01)/e-nolv(09-10)/e-nofalse(08-30) = 3
	if s.RecentAdded != 3 {
		t.Errorf("recent_added=%d want 3", s.RecentAdded)
	}
	// RenderMarkdown 关键行在文
	md := RenderMarkdown(s)
	for _, want := range []string{"total: 5", "by_kind:", "stale_buckets: <30d=2 30-90d=1 >90d=1 missing=1", "recent_added_30d: 3"} {
		if !strings.Contains(md, want) {
			t.Errorf("snapshot missing %q; got:\n%s", want, md)
		}
	}
}

// TestAssemble T2：模板拼装——快照 fenced 在文、AI 标注行在头部、四节齐全。
func TestAssemble(t *testing.T) {
	ai := "## 分析\n主导类 X。\n## 退役候选\n- a | 被取代 | 由 b 覆盖\n## 候选新条目\n- t1\n```kbmeta-candidates\nt1 | r1\n```"
	out := assemble("2026-09-24", "glm-test", "total: 5\n", ai)
	for _, want := range []string{
		"> AI 生成（glm-test），未经独立复审",
		"## 统计快照", "```\ntotal: 5\n```",
		"## 分析", "## 退役候选", "## 候选新条目",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("assemble missing %q; got:\n%s", want, out)
		}
	}
	// 快照块先于 AI 节（AI 不可写区在前）
	if strings.Index(out, "## 统计快照") > strings.Index(out, "## 分析") {
		t.Error("快照节应先于 AI 分析节")
	}
}

// fakeRun 捕获 prompt/工具集并回固定 AI 输出（Runner seam）。
type fakeRun struct {
	gotPrompt string
	gotSpecs  []analyst.ToolSpec
	gotSystem string
	output    string
}

func (f *fakeRun) run(_ context.Context, _ *analyst.Client, _ analyst.ToolExecutor,
	specs []analyst.ToolSpec, sys string, msgs []analyst.Message,
	cb analyst.LoopCallbacks) (analyst.LoopResult, error) {
	f.gotSpecs = specs
	f.gotSystem = sys
	if len(msgs) > 0 {
		f.gotPrompt = string(msgs[0].Content)
	}
	cb.OnDelta(f.output)
	return analyst.LoopResult{Grounded: true}, nil
}

// TestGenerateFakeRunner T3：prompt 含统计块、输出路径模式正确、待归纳追加行格式对。
func TestGenerateFakeRunner(t *testing.T) {
	tmp := t.TempDir()
	entries := filepath.Join(tmp, "条目")
	reportDir := filepath.Join(tmp, "报告")
	inbox := filepath.Join(tmp, "待归纳-分析员.md")
	if err := os.MkdirAll(entries, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEntry(t, entries, "e-fresh", "pitfall", "high", "verified", "2026-09-01", "2026-09-20",
		[]string{"grpc"}, []string{"internal/adapter"}, true)

	fr := &fakeRun{output: "## 分析\nok\n## 退役候选\n无\n## 候选新条目\n提议\n```kbmeta-candidates\nnew-entry-a | 缺口理由甲\nnew-entry-b | 理由|带管\n```"}
	deps := Deps{
		Client:     analyst.NewClient("http://x", "k", "glm-test"),
		Tools:      &analyst.Tools{},
		EntriesDir: entries,
		ReportDir:  reportDir,
		InboxPath:  inbox,
		Run:        fr.run,
		Now:        func() time.Time { return fixtureNow },
	}
	path, err := Generate(context.Background(), deps)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// 路径模式
	wantPath := filepath.Join(reportDir, "kb-meta-2026-09-24.md")
	if path != wantPath {
		t.Fatalf("path=%q want %q", path, wantPath)
	}
	// prompt 含统计块
	if !strings.Contains(fr.gotPrompt, "total: 1") {
		t.Errorf("prompt missing stats block; got %q", fr.gotPrompt)
	}
	// 工具子集=3（grep_kb/read_doc/query_ro）
	if len(fr.gotSpecs) != 3 {
		t.Errorf("tool specs=%d want 3", len(fr.gotSpecs))
	}
	// 报告文件在档且含四节
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !strings.Contains(string(data), "## 统计快照") || !strings.Contains(string(data), "## 退役候选") {
		t.Errorf("report sections missing; got:\n%s", data)
	}
	// 待归纳追加行格式 | date | source=kb-meta | title | reason |
	inboxData, err := os.ReadFile(inbox)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	for _, want := range []string{
		"| 2026-09-24 | source=kb-meta | new-entry-a | 缺口理由甲 |",
		"| 2026-09-24 | source=kb-meta | new-entry-b | 理由\\|带管 |",
	} {
		if !strings.Contains(string(inboxData), want) {
			t.Errorf("inbox missing %q; got:\n%s", want, inboxData)
		}
	}
}

// TestGenerateNoCandidates 无候选块 → 不追加收件箱。
func TestGenerateNoCandidates(t *testing.T) {
	tmp := t.TempDir()
	entries := filepath.Join(tmp, "条目")
	inbox := filepath.Join(tmp, "待归纳-分析员.md")
	if err := os.MkdirAll(entries, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEntry(t, entries, "e1", "pitfall", "low", "verified", "2026-09-01", "2026-09-20", nil, nil, true)
	fr := &fakeRun{output: "## 分析\nok\n## 退役候选\n无\n## 候选新条目\n无\n```kbmeta-candidates\n```"}
	_, err := Generate(context.Background(), Deps{
		Client: analyst.NewClient("http://x", "k", "m"), Tools: &analyst.Tools{},
		EntriesDir: entries, ReportDir: filepath.Join(tmp, "报告"), InboxPath: inbox,
		Run: fr.run, Now: func() time.Time { return fixtureNow },
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := os.Stat(inbox); !os.IsNotExist(err) {
		t.Errorf("inbox should not be created when no candidates")
	}
}

// TestParseCandidatesUnclosed_D457：S5——sensenova EOF 截断未闭合块容忍解析+malformed 标记。
func TestParseCandidatesUnclosed_D457(t *testing.T) {
	// 闭合块正常解析。
	c, mal := parseCandidates("## 候选新条目\n无\n```kbmeta-candidates\n甲 | 乙\n```\n")
	if mal || len(c) != 1 || c[0].title != "甲" {
		t.Fatalf("closed parse wrong: %v mal=%v", c, mal)
	}
	// 未闭合（EOF 截断）→ 容忍解析到尾 + malformed=true。
	c, mal = parseCandidates("## 候选新条目\nx\n```kbmeta-candidates\n丙 | 丁\n戊 | 己")
	if !mal || len(c) != 2 || c[1].title != "戊" {
		t.Fatalf("unclosed parse wrong: %v mal=%v", c, mal)
	}
	// 完全无块。
	if c, mal = parseCandidates("no block"); c != nil || mal {
		t.Fatalf("no block wrong: %v %v", c, mal)
	}
}

// TestResolveClientEmptyModel D-591 S7：DB+env model 双空 → 报错（nil store 纯 env 路
// ——apiKey 非空、model 空：禁静默构造空模型 client）。
func TestResolveClientEmptyModel(t *testing.T) {
	if _, err := ResolveClient(context.Background(), nil, "https://env.example.com", "env-key", ""); err == nil {
		t.Fatal("model 双空应报错（fail-closed）")
	}
}
