package kbsearch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const entryA = `---
id: binance-earn-amount-empty
kind: pitfall
status: observed
keywords: ["-1102", "amount empty", "binance earn"]
summary: 尘埃额格式化归零触发 -1102 amount empty
---
body
`

const entryB = `---
id: generic-binance
kind: pitfall
status: verified
keywords: ["binance"]
summary: 泛词条目
---
`

const entryRetired = `---
id: old-pit
kind: pitfall
status: retired
keywords: ["-1102", "amount empty"]
summary: 退休条目不参与
---
`

func TestLoadAndMatch(t *testing.T) {
	dir := seedDir(t, map[string]string{
		"a.md": entryA, "b.md": entryB, "r.md": entryRetired, "junk.md": "no fm",
	})
	entries, err := Load(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
	// 单短词命中不达标（"binance" <12 字符且 hits=1）。
	if h := Match(entries, "binance spot post rejected"); h != nil {
		t.Fatalf("短词单命中误标: %+v", h)
	}
	// ≥2 命中或指纹词命中。
	h := Match(entries, `code -1102: "amount empty"`)
	if h == nil || h.ID != "binance-earn-amount-empty" {
		t.Fatalf("want A, got %+v", h)
	}
	if h.Hits != 2 {
		t.Fatalf("hits=%d", h.Hits) // -1102 + amount empty 两中
	}
	// retired 已被 Load 剔除（r.md 关键词相同不抢位）。
}

func TestSuffixFailOpen(t *testing.T) {
	if s := Suffix("/nonexistent-dir", "anything"); s != "" {
		t.Fatal("读败须空串")
	}
	dir := seedDir(t, map[string]string{"a.md": entryA})
	s := Suffix(dir, `-1102 amount empty`)
	if !strings.HasPrefix(s, " kb:binance-earn-amount-empty ") {
		t.Fatalf("suffix=%q", s)
	}
}
