package dashboard

// service_analyst_test.go D-091：AppendAnalystNote 并发安全 + 未配置拒绝 + 路径非法拒绝。
// R3 返修②：resolveAiConfig env 回落 / maskAPIKey / DB 优先+空key不覆盖（DSN-gated）。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
)

func newAnalystService(knRoot, notePath string) *Service {
	return &Service{analyst: &analystDeps{knRoot: knRoot, notePath: notePath}}
}

func TestAppendAnalystNoteRejectsEmpty(t *testing.T) {
	s := newAnalystService(t.TempDir(), "docs/经验库/待归纳-分析员.md")
	_, err := s.AppendAnalystNote(context.Background(), connect.NewRequest(&v1.AppendAnalystNoteRequest{}))
	if err == nil {
		t.Fatal("expected error for empty question/summary")
	}
}

func TestAppendAnalystNoteRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	s := &Service{analyst: &analystDeps{knRoot: dir, notePath: "../../etc/passwd"}}
	_, err := s.AppendAnalystNote(context.Background(), connect.NewRequest(&v1.AppendAnalystNoteRequest{
		Question: "q", Summary: "s",
	}))
	if err == nil {
		t.Fatal("expected path-escape rejection")
	}
}

func TestAppendAnalystNoteWrites(t *testing.T) {
	dir := t.TempDir()
	rel := "docs/经验库/待归纳-分析员.md"
	_ = os.MkdirAll(filepath.Join(dir, "docs", "经验库"), 0755)
	s := newAnalystService(dir, rel)
	_, err := s.AppendAnalystNote(context.Background(), connect.NewRequest(&v1.AppendAnalystNoteRequest{
		Question:  "为什么 #264 浮亏？",
		Summary:   "跨 broker 基差 $32.67",
		Citations: []*v1.AnalystCitation{{Kind: "sql", Ref: "SELECT 1", Excerpt: "1 row"}},
	}))
	if err != nil {
		t.Fatalf("AppendAnalystNote error: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, rel))
	if !strings.Contains(string(data), "为什么 #264 浮亏？") {
		t.Fatalf("note not written: %s", string(data))
	}
	if !strings.Contains(string(data), "跨 broker 基差") {
		t.Fatalf("summary not written: %s", string(data))
	}
}

func TestAppendNoteConcurrent(t *testing.T) {
	dir := t.TempDir()
	rel := "docs/经验库/待归纳-分析员.md"
	full := filepath.Join(dir, rel)
	_ = os.MkdirAll(filepath.Dir(full), 0755)
	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_ = appendNote(full, strings.Repeat("x", 100))
		}(i)
	}
	wg.Wait()
	data, _ := os.ReadFile(full)
	// 每次写 100 字符 'x'，50 次 = 5000 字符（无丢失/交错）
	if got := len(string(data)); got != n*100 {
		t.Fatalf("concurrent append lost data: got %d bytes, want %d", got, n*100)
	}
}

func TestAskAnalystRejectsUnconfigured(t *testing.T) {
	s := &Service{} // analyst nil
	err := s.AskAnalyst(context.Background(), connect.NewRequest(&v1.AskAnalystRequest{Question: "q"}), nil)
	if err == nil {
		t.Fatal("expected failed_precondition for unconfigured analyst")
	}
}

// --- R3 返修② 单测 ---

func TestMaskAPIKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"ab", "ab"},
		{"abcd", "abcd"},
		{"abcde", "****bcde"},
		{"sk-1234567890", "****7890"},
	}
	for _, c := range cases {
		if got := maskAPIKey(c.in); got != c.want {
			t.Errorf("maskAPIKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTruncateStr(t *testing.T) {
	if got := truncateStr("hello", 10); got != "hello" {
		t.Errorf("truncateStr short: got %q", got)
	}
	if got := truncateStr("hello world", 5); got != "hello…" {
		t.Errorf("truncateStr long: got %q", got)
	}
}

// TestResolveAiConfigEnvFallback nil store → env 回落（env apiKey 非空 → 成功）。
func TestResolveAiConfigEnvFallback(t *testing.T) {
	s := &Service{
		analyst: &analystDeps{
			envBaseURL: "https://env.example.com",
			envModel:   "env-model",
			envAPIKey:  "env-key",
		},
		// store nil → 跳过 DB，纯 env
	}
	c, err := s.resolveAiConfig(context.Background())
	if err != nil {
		t.Fatalf("env fallback error: %v", err)
	}
	if c.APIKey != "env-key" || c.BaseURL != "https://env.example.com" || c.Model != "env-model" {
		t.Fatalf("env fallback wrong: %+v", c)
	}
}

// TestResolveAiConfigBothEmpty nil store + env apiKey 空 → failed_precondition。
func TestResolveAiConfigBothEmpty(t *testing.T) {
	s := &Service{
		analyst: &analystDeps{envBaseURL: "https://env.example.com", envModel: "m"},
	}
	_, err := s.resolveAiConfig(context.Background())
	if err == nil {
		t.Fatal("expected failed_precondition when both DB and env api_key empty")
	}
}

// TestResolveAiConfigNilDeps → failed_precondition。
func TestResolveAiConfigNilDeps(t *testing.T) {
	s := &Service{}
	_, err := s.resolveAiConfig(context.Background())
	if err == nil {
		t.Fatal("expected error for nil deps")
	}
}

// TestAppendAnalystNoteTableFormat R5 返修②：落盘格式为表格行。
func TestAppendAnalystNoteTableFormat(t *testing.T) {
	dir := t.TempDir()
	rel := "docs/经验库/待归纳-分析员.md"
	_ = os.MkdirAll(filepath.Join(dir, "docs", "经验库"), 0755)
	s := newAnalystService(dir, rel)
	_, err := s.AppendAnalystNote(context.Background(), connect.NewRequest(&v1.AppendAnalystNoteRequest{
		Question:  "测试问题",
		Summary:   "测试结论",
		Citations: []*v1.AnalystCitation{{Kind: "sql", Ref: "SELECT 1", Excerpt: "1 row"}},
	}))
	if err != nil {
		t.Fatalf("AppendAnalystNote error: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, rel))
	line := strings.TrimSpace(string(data))
	// 应为表格行格式：| 日期 | 问题 | 结论 | 引用数 |
	if !strings.HasPrefix(line, "| ") || !strings.HasSuffix(line, " |") {
		t.Fatalf("expected table row format, got: %q", line)
	}
	if !strings.Contains(line, "测试问题") || !strings.Contains(line, "测试结论") {
		t.Fatalf("table row missing content: %q", line)
	}
	if !strings.Contains(line, "| 1 |") {
		t.Fatalf("table row missing citation count: %q", line)
	}
	// 不应含 markdown 小节标记
	if strings.Contains(line, "## ") || strings.Contains(line, "- 日期:") {
		t.Fatalf("old markdown format leaked: %q", line)
	}
}
