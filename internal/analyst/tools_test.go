package analyst

// tools_test.go 安全护栏单测（设计 §6 安全清单）：
// query_ro 拒 UPDATE/拒多语句/无 DSN 拒；read_doc 路径逃逸拒/白名单外拒；rate 桶满拒。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
