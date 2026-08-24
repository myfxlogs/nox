package dashboard

// service_analyst_test.go D-091：AppendAnalystNote 并发安全 + 未配置拒绝 + 路径非法拒绝。

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
