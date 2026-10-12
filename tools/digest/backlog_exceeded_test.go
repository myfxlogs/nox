// d172_backlog_test.go —— D-172：积压闸三锚（超阈发 warn / 未超静默 / ✅ 行不计数）。
package main

import (
	"os"
	"strings"
	"testing"
)

func TestD172_BacklogExceeded(t *testing.T) {
	if !backlogExceeded(31, 1) {
		t.Error("行数>30 应触发")
	}
	if backlogExceeded(30, 1) {
		t.Error("行数=30 不应触发（界为 >30）")
	}
	if !backlogExceeded(2, 101) {
		t.Error("单行次数>100 应触发")
	}
	if backlogExceeded(2, 100) {
		t.Error("单行次数=100 不应触发（界为 >100）")
	}
	if backlogExceeded(0, 0) {
		t.Error("空箱不应触发")
	}
}

func TestD172_BacklogStats(t *testing.T) {
	agg := []pendingEventAgg{{Count: 3}, {Count: 150}, {Count: 7}}
	rows, maxCount := backlogStats(agg)
	if rows != 3 || maxCount != 150 {
		t.Fatalf("got rows=%d max=%d, want 3/150", rows, maxCount)
	}
}

// ✅ 行不计数：parsePendingHistory 只把无 ✅ 的行回 active——闸只吃 active 集。
func TestD172_ResolvedRowsNotCounted(t *testing.T) {
	md := `# 待归纳事件

| 日期 | 来源 | 事件摘要 | 次数 |
|------|------|----------|------|
| 2026-10-01 | alerts | a | 200 ⚠ 重复 ✅ 已修复（D-999） |
| 2026-10-02 | alerts | b | 50 ⚠ 重复 ✅ 噪音判否 |
| 2026-10-03 | alerts | c | 3 ⚠ 重复 |

## staleness 报告
`
	path := t.TempDir() + "/待归纳.md"
	if err := os.WriteFile(path, []byte(md), 0644); err != nil {
		t.Fatal(err)
	}
	active, resolved := parsePendingHistory(path)
	if len(resolved) != 2 || len(active) != 1 {
		t.Fatalf("active=%d resolved=%d, want 1/2", len(active), len(resolved))
	}
	agg := aggregatePending(active)
	rows, maxCount := backlogStats(agg)
	if rows != 1 || maxCount != 3 {
		t.Fatalf("✅ 行漏入计数：rows=%d max=%d, want 1/3", rows, maxCount)
	}
	if backlogExceeded(rows, maxCount) {
		t.Fatal("✅ 行不应计入超阈判定")
	}
	// 归档签名核对：两 resolved 行的 verdict 被剥离 ⚠ 标记后保留。
	for _, r := range resolved {
		if strings.Contains(r.Verdict, "⚠") {
			t.Fatalf("verdict 携 ⚠ 残标：%q", r.Verdict)
		}
	}
}
