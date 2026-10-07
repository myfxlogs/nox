package analyst

// tools_test.go 安全护栏单测（设计 §6 安全清单）：
// query_ro 拒 UPDATE/拒多语句/无 DSN 拒；read_doc 路径逃逸拒/白名单外拒；rate 桶满拒。
// R1 返修②：补 DSN-gated 真连 happy path 单测（此前只覆盖拒绝路径，happy path 零覆盖是驱动未注册 bug 漏网原因）。

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"arb/internal/testutil"
)

func TestQueryRORejectsNonSelect(t *testing.T) {
	tools := &Tools{}
	cases := []string{
		`{"sql":"UPDATE positions SET x=1"}`,
		`{"sql":"DELETE FROM positions"}`,
		`{"sql":"DROP TABLE positions"}`,
		`{"sql":"INSERT INTO positions VALUES(1)"}`,
	}
	for i, in := range cases {
		res, _, err := tools.execQueryRO(context.Background(), ToolCall{ID: "c1", Input: in})
		if err != nil {
			t.Fatalf("case %d unexpected error: %v", i, err)
		}
		if !res.IsError {
			t.Fatalf("case %d: expected rejection, got success: %s", i, res.Content)
		}
	}
}

func TestQueryRORejectsMultiStatement(t *testing.T) {
	tools := &Tools{}
	res, _, _ := tools.execQueryRO(context.Background(), ToolCall{ID: "c1", Input: `{"sql":"SELECT 1; DROP TABLE positions"}`})
	if !res.IsError {
		t.Fatalf("expected multi-statement rejection, got: %s", res.Content)
	}
}

func TestQueryRORejectsNoDSN(t *testing.T) {
	tools := &Tools{ReadOnlyDSN: ""}
	res, _, _ := tools.execQueryRO(context.Background(), ToolCall{ID: "c1", Input: `{"sql":"SELECT 1"}`})
	if !res.IsError {
		t.Fatalf("expected no-DSN rejection, got: %s", res.Content)
	}
}

func TestReadDocRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	// 创建 docs 子目录 + 一个诱饵文件在根
	_ = os.MkdirAll(filepath.Join(dir, "docs"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("SECRET"), 0644)
	tools := &Tools{KnRoot: dir}
	// ../ 逃逸
	res, _, _ := tools.execReadFile(context.Background(), ToolCall{ID: "c1", Input: `{"path":"../secret.txt"}`}, "docs")
	if !res.IsError {
		t.Fatalf("expected path-escape rejection, got: %s", res.Content)
	}
	// 绝对路径
	res, _, _ = tools.execReadFile(context.Background(), ToolCall{ID: "c2", Input: `{"path":"/etc/passwd"}`}, "docs")
	if !res.IsError {
		t.Fatalf("expected absolute-path rejection, got: %s", res.Content)
	}
	// 白名单外（internal 不在 docs 白名单）
	_ = os.MkdirAll(filepath.Join(dir, "internal"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "internal", "x.go"), []byte("package x"), 0644)
	res, _, _ = tools.execReadFile(context.Background(), ToolCall{ID: "c3", Input: `{"path":"internal/x.go"}`}, "docs")
	if !res.IsError {
		t.Fatalf("expected whitelist rejection, got: %s", res.Content)
	}
}

func TestReadDocAllowsWhitelist(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "docs", "经验库"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "docs", "经验库", "a.md"), []byte("# 条目"), 0644)
	tools := &Tools{KnRoot: dir}
	res, _, err := tools.execReadFile(context.Background(), ToolCall{ID: "c1", Input: `{"path":"docs/经验库/a.md"}`}, "docs")
	if err != nil || res.IsError {
		t.Fatalf("expected success, got err=%v res=%s", err, res.Content)
	}
}

// TestGrepKB_SuffixFilter D-089：--include 限定移 Go 侧——同 pattern 三文件命中，
// 结果只含 a.md/c.go（后缀过滤证），b.txt 被滤。
func TestGrepKB_SuffixFilter(t *testing.T) {
	dir := t.TempDir()
	pattern := "NEEDLE_XAU"
	files := map[string]string{"a.md": "hit " + pattern, "b.txt": "hit " + pattern, "c.go": "hit " + pattern}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	tools := &Tools{KnRoot: dir}
	res, _, err := tools.execGrepKB(context.Background(), ToolCall{ID: "c1", Input: `{"pattern":"` + pattern + `"}`})
	if err != nil || res.IsError {
		t.Fatalf("grep_kb failed: err=%v res=%s", err, res.Content)
	}
	got := strings.Split(res.Content, "\n")
	sort.Strings(got)
	want := []string{filepath.Join(dir, "a.md"), filepath.Join(dir, "c.go")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("后缀过滤结果 = %v, want %v（b.txt 应被滤）", got, want)
	}
}

func TestRateLimiterRejectsWhenFull(t *testing.T) {
	rl := NewRateLimiter(2)
	if !rl.Allow() {
		t.Fatal("first allow should pass")
	}
	if !rl.Allow() {
		t.Fatal("second allow should pass")
	}
	if rl.Allow() {
		t.Fatal("third allow should be rejected (bucket full)")
	}
}

func TestRateLimiterUnlimited(t *testing.T) {
	rl := NewRateLimiter(0)
	for i := 0; i < 100; i++ {
		if !rl.Allow() {
			t.Fatalf("unlimited limiter rejected at %d", i)
		}
	}
}

// TestQueryRORealConnect DSN-gated 真连 happy path（R1 返修②）。
// 设 ARB_TEST_PG_DSN 时跑 SELECT 1 全链（验证 pgxpool 驱动注册 + 查询 + 行解析）；
// 未设时 skip（go test ./... 无 PG 环境仍全过）。
func TestQueryRORealConnect(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	tools := &Tools{ReadOnlyDSN: dsn}
	res, cites, err := tools.execQueryRO(context.Background(), ToolCall{ID: "c1", Input: `{"sql":"SELECT 1 AS one"}`})
	if err != nil {
		t.Fatalf("execQueryRO error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if len(cites) == 0 {
		t.Fatal("expected citation")
	}
	// 内容应含 "one" 列 + 值 1（pgx 返回 int32/int64，JSON 序列化后为数字）
	if !strings.Contains(res.Content, `"one"`) {
		t.Fatalf("result missing 'one' column: %s", res.Content)
	}
}

// --- D-100 llm_probe 锚 ---

func fakeProbe(ms, chars int, stop string) ProbeCaller {
	return func(_ context.Context, _ string, _ int, _ bool) (int, int, string, string, error) {
		return ms, chars, stop, "none", nil
	}
}

func TestLLMProbeReturnsStructured(t *testing.T) {
	tools := &Tools{ProbeCaller: fakeProbe(1234, 56, "max_tokens")}
	res, cites, err := tools.execLLMProbe(context.Background(), ToolCall{ID: "c1", Input: `{"prompt":"ping","max_tokens":512}`})
	if err != nil || res.IsError {
		t.Fatalf("probe: err=%v res=%s", err, res.Content)
	}
	for _, kw := range []string{"elapsed=", "chars=56", "stop=max_tokens", "err=none"} {
		if !strings.Contains(res.Content, kw) {
			t.Fatalf("结构化文本缺 %q: %s", kw, res.Content)
		}
	}
	if len(cites) == 0 {
		t.Fatal("expected citation")
	}
}

func TestLLMProbeRejectsBadParams(t *testing.T) {
	tools := &Tools{ProbeCaller: fakeProbe(1, 1, "end_turn")}
	long := strings.Repeat("超", 501)
	for _, in := range []string{
		`{"prompt":"` + long + `","max_tokens":512}`, // prompt 超 500
		`{"prompt":"p","max_tokens":128}`,            // 低于 256
		`{"prompt":"p","max_tokens":16384}`,          // 高于 8192
		`{"prompt":"","max_tokens":512}`,             // 空 prompt
	} {
		res, _, _ := tools.execLLMProbe(context.Background(), ToolCall{ID: "c1", Input: in})
		if !res.IsError {
			t.Fatalf("expected rejection for %s, got: %s", in[:40], res.Content)
		}
	}
}

func TestLLMProbeRateLimit(t *testing.T) {
	tools := &Tools{ProbeCaller: fakeProbe(1, 1, "end_turn")}
	call := ToolCall{ID: "c1", Input: `{"prompt":"p","max_tokens":512}`}
	for i := 0; i < 10; i++ {
		res, _, _ := tools.execLLMProbe(context.Background(), call)
		if res.IsError {
			t.Fatalf("call %d unexpectedly rejected: %s", i+1, res.Content)
		}
	}
	res, _, _ := tools.execLLMProbe(context.Background(), call)
	if !res.IsError || !strings.Contains(res.Content, "限额") {
		t.Fatalf("11th call should hit 限额, got: %s", res.Content)
	}
}

func TestLLMProbeInSpecs(t *testing.T) {
	for _, s := range ToolSpecs() {
		if s.Name == "llm_probe" {
			return
		}
	}
	t.Fatal("ToolSpecs 缺 llm_probe")
}
