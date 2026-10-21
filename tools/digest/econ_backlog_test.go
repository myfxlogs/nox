// econ_backlog_test.go —— 消化链建制①锚：未裁定行计数/最老天数解析/✅消解行剔除/超阈判。
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEconBacklogStats(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "待归纳.md")
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	content := `# 头
- 机械行说明行（非事件——无 settled 不计日期但计行）
- pos=1 net=-3.5 settled=2026-10-10 key=ek1
- pos=2 net=-1.0 settled=2026-10-19 key=ek2 ✅ canon:e-x
- [LLM草稿] 某草稿行
`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rows, oldest := econBacklogStats(p, now)
	if rows != 3 { // 未消解行=机械说明行+ek1+LLM草稿行（ek2 已 ✅ 剔除）
		t.Fatalf("rows=%d want 3", rows)
	}
	if oldest != 11 {
		t.Fatalf("oldest=%d want 11（2026-10-10→10-21）", oldest)
	}
	// 文件缺=零积压
	if r, d := econBacklogStats(filepath.Join(dir, "none.md"), now); r != 0 || d != 0 {
		t.Fatal("文件缺应零积压")
	}
}
