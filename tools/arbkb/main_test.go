package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// fixtureEntry 构造一个测试用 frontmatter 条目字节。
func fixtureEntry(id, kind, risk, status, summary string, keywords []string) []byte {
	kw := ""
	for _, k := range keywords {
		kw += "  - " + k + "\n"
	}
	return []byte(`---
schema_version: 2
id: ` + id + `
kind: ` + kind + `
risk: ` + risk + `
status: ` + status + `
keywords:
` + kw + `summary: ` + summary + `
first_seen: 2026-08-21
last_verified: 2026-08-21
---
`)
}

// TestGoldenFile 固定 fixture → 期望字节输出
func TestGoldenFile(t *testing.T) {
	entries := []*Entry{
		{ID: "alpha", Kind: "pitfall", Risk: "high", Status: "verified",
			Keywords: []string{"foo", "bar"}, Summary: "alpha summary"},
		{ID: "beta", Kind: "invariant", Risk: "critical", Status: "verified",
			Keywords: []string{"baz"}, Summary: "beta|summary"},
	}
	output := render(entries)
	expected := header +
		"| alpha | pitfall | foo bar | alpha summary | verified | high |\n" +
		"| beta | invariant | baz | beta\\|summary | verified | critical |\n"
	if !bytes.Equal(output, []byte(expected)) {
		t.Errorf("render mismatch:\ngot:\n%s\nwant:\n%s", output, expected)
	}
}

// TestIdempotent 二次 gen 无写入
func TestIdempotent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "entries") // 条目子目录，与索引分离
	idx := filepath.Join(root, "index.md")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), fixtureEntry("a", "pitfall", "low", "verified", "a sum", []string{"x"}), 0644); err != nil {
		t.Fatal(err)
	}
	origEntriesDir := entriesDir
	origIndexFile := indexFile
	entriesDir = dir
	indexFile = idx
	defer func() {
		entriesDir = origEntriesDir
		indexFile = origIndexFile
	}()

	if err := gen(); err != nil {
		t.Fatalf("first gen: %v", err)
	}
	first, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	if err := gen(); err != nil {
		t.Fatalf("second gen: %v", err)
	}
	second, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("second gen modified file (not idempotent)")
	}
}

// TestPipeEscape | 转义
func TestPipeEscape(t *testing.T) {
	e := &Entry{ID: "test|pipe", Kind: "pitfall", Risk: "low", Status: "verified",
		Keywords: []string{"a|b"}, Summary: "sum|with|pipe"}
	row := renderRow(e)
	if !bytes.Contains([]byte(row), []byte("test\\|pipe")) {
		t.Errorf("id pipe not escaped: %s", row)
	}
	if !bytes.Contains([]byte(row), []byte("a\\|b")) {
		t.Errorf("keyword pipe not escaped: %s", row)
	}
	if !bytes.Contains([]byte(row), []byte("sum\\|with\\|pipe")) {
		t.Errorf("summary pipe not escaped: %s", row)
	}
}

// TestGenCheckMismatch --check 不一致路径
func TestGenCheckMismatch(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "entries")
	idx := filepath.Join(root, "index.md")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idx, []byte("stale content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), fixtureEntry("a", "pitfall", "low", "verified", "a sum", []string{"x"}), 0644); err != nil {
		t.Fatal(err)
	}
	origEntriesDir := entriesDir
	origIndexFile := indexFile
	entriesDir = dir
	indexFile = idx
	defer func() {
		entriesDir = origEntriesDir
		indexFile = origIndexFile
	}()

	if err := genCheck(); err == nil {
		t.Error("genCheck should fail on mismatch, got nil")
	}
}

// TestBudgetExceed 预算超限报错
func TestBudgetExceed(t *testing.T) {
	// 构造超 200 行的输出
	var entries []*Entry
	for i := 0; i < 250; i++ {
		entries = append(entries, &Entry{
			ID:      "entry-" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Kind:    "pitfall", Risk: "low", Status: "verified",
			Keywords: []string{"k"}, Summary: "s",
		})
	}
	output := render(entries)
	if err := checkBudget(output); err == nil {
		t.Error("checkBudget should fail on >200 lines, got nil")
	}
}
