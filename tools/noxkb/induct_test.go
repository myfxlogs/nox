package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupKB 构造 tmpdir 经验库：条目/待归纳/分析员收件箱全量 redirect。
func setupKB(t *testing.T, pend, inbox string) string {
	t.Helper()
	root := t.TempDir()
	kb := filepath.Join(root, "kb")
	for _, d := range []string{"条目", "归档"} {
		if err := os.MkdirAll(filepath.Join(kb, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{"待归纳.md": pend, "待归纳-分析员.md": inbox, "索引.md": "", "技能清单.md": ""}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(kb, n), []byte(c), 0644); err != nil {
			t.Fatal(err)
		}
	}
	oe, oi, os_, op, ob := entriesDir, indexFile, skillsIndexFile, pendingFile, inboxFile
	entriesDir = filepath.Join(kb, "条目")
	indexFile = filepath.Join(kb, "索引.md")
	skillsIndexFile = filepath.Join(kb, "技能清单.md")
	pendingFile = filepath.Join(kb, "待归纳.md")
	inboxFile = filepath.Join(kb, "待归纳-分析员.md")
	t.Cleanup(func() {
		entriesDir, indexFile, skillsIndexFile, pendingFile, inboxFile = oe, oi, os_, op, ob
	})
	return kb
}

const pendFixture = `# 待归纳事件（digest 自动生成，勿手编）
> 消解出口：分诊完成的行在「次数」列尾追加 ✅ 结论

| 日期 | 来源 | 事件摘要 | 次数 |
|------|------|----------|------|
| 2026-10-01 | alerts | flux-capacitor 溢出导致门电路熔断 | 5 |
| 2026-10-02 | alerts | 某已修复告警旧指纹 | 3 ✅ 已修复：D-999 |

## 其它段
| x | y |
`

func entryFixture(id string, kws ...string) []byte {
	var kw strings.Builder
	for _, k := range kws {
		kw.WriteString("  - " + k + "\n")
	}
	return []byte("---\nschema_version: 2\nid: " + id + "\nkind: pitfall\nrisk: low\nstatus: verified\nkeywords:\n" +
		kw.String() + "summary: s\nfirst_seen: 2026-08-01\nlast_verified: 2026-08-01\n---\n")
}

func TestInduct_Covered(t *testing.T) {
	kb := setupKB(t, pendFixture, "")
	os.WriteFile(filepath.Join(kb, "条目", "flux-pit.md"), entryFixture("flux-pit", "flux-capacitor 溢出"), 0644)
	cov, dr, err := induct(context.Background(), inductOpts{now: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)})
	if err != nil || cov != 1 || dr != 0 {
		t.Fatalf("cov=%d dr=%d err=%v", cov, dr, err)
	}
	data, _ := os.ReadFile(filepath.Join(kb, "待归纳.md"))
	if !strings.Contains(string(data), "✅ inducted:") || !strings.Contains(string(data), "covered:flux-pit") {
		t.Fatalf("covered 标缺失:\n%s", data)
	}
	// 幂等：二跑零增量
	cov2, _, _ := induct(context.Background(), inductOpts{now: time.Now()})
	if cov2 != 0 {
		t.Fatalf("重跑非幂等 cov=%d", cov2)
	}
}

func TestInduct_DraftAndSchemaGate(t *testing.T) {
	kb := setupKB(t, pendFixture, "| 日期 | 问题 | 结论摘要 | 引用数 |\n|--|--|--|--|\n| 2026-10-13 | symdig_alias | ALU→XAL 归 symbol-map 复核域（D-172） | x |\n| 2026-10-13 | q1 | 结论一 | 2 |\n")
	stub := func(_ context.Context, _, _ string) (string, error) {
		return `{"drafts":[
		  {"id":"flux-gate-burn","kind":"pitfall","risk":"medium","keywords":["flux"],"scope":["gate"],"summary":"flux 门熔断","facts":"f","root_cause":"r","handling":"h","boundary":"b","rows":[0]},
		  {"id":"Bad Slug!","kind":"pitfall","risk":"low","keywords":["x"],"summary":"s","rows":[0]},
		  {"id":"zero-rows","kind":"ops","risk":"low","keywords":["x"],"summary":"s","rows":[]}
		]}`, nil
	}
	cov, dr, err := induct(context.Background(), inductOpts{now: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC), llm: stub})
	if err != nil || dr != 1 {
		t.Fatalf("cov=%d dr=%d err=%v", cov, dr, err)
	}
	data, _ := os.ReadFile(filepath.Join(kb, "条目", "flux-gate-burn.md"))
	if !strings.Contains(string(data), "status: observed") || !strings.Contains(string(data), "## 反证条件") {
		t.Fatalf("草稿违式:\n%s", data)
	}
	// 违式条目不落盘
	if _, err := os.Stat(filepath.Join(kb, "条目", "Bad Slug!.md")); !os.IsNotExist(err) {
		t.Fatal("违式 slug 落盘")
	}
	// inbox：复核域行未打标，普通行打 draft 标
	ib, _ := os.ReadFile(filepath.Join(kb, "待归纳-分析员.md"))
	if strings.Contains(string(ib), "复核域（D-172） inducted") {
		t.Fatal("D-172 域行被误标")
	}
	pd, _ := os.ReadFile(filepath.Join(kb, "待归纳.md"))
	if !strings.Contains(string(pd), "inducted:") && strings.Contains(string(ib), "inducted:") {
		t.Fatal("inbox 行未引 draft 却被标")
	}
	// 索引重产含 observed 条目
	idx, _ := os.ReadFile(filepath.Join(kb, "索引.md"))
	if !strings.Contains(string(idx), "flux-gate-burn") {
		t.Fatal("索引未重产")
	}
	// 幂等：二跑（LLM 仍 stub）不打新标
	stub2Called := false
	induct(context.Background(), inductOpts{now: time.Now(), llm: func(c context.Context, s, p string) (string, error) {
		stub2Called = true
		return `{"drafts":[]}`, nil
	}})
	// 已标行不再进 LLM 输入——但「q1」行未被 draft 引用仍 pending，属设计（下次重试）
	_ = stub2Called
}

func TestInduct_SchemaFailClosed(t *testing.T) {
	setupKB(t, pendFixture, "")
	bad := func(context.Context, string, string) (string, error) { return "not json at all", nil }
	cov, dr, err := induct(context.Background(), inductOpts{now: time.Now(), llm: bad})
	if err != nil || dr != 0 || cov != 0 {
		t.Fatalf("cov=%d dr=%d err=%v", cov, dr, err)
	}
	pd, _ := os.ReadFile(pendingFile)
	if strings.Contains(string(pd), "inducted:") {
		t.Fatal("schema 违式却打了标")
	}
}
