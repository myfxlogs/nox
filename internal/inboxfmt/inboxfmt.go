// inboxfmt —— D-534 B8：收件箱（待归纳-分析员.md markdown 表）写侧/计数共享位。
// 消 sysretro/explorer/symdig 三份 appendInbox+cleanCell 拷贝为一处。
// 行格式 v2：| date | source | title | reason | action | status | resolution |；
// 新行一律 status=pending + 空 resolution；无 status 列旧行按 pending 计（向后兼容）。
// 红线：收件箱行永不触发代码/配置写路径。
package inboxfmt

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// source 枚举定死（D-534 S4；calib_scoreboard 随 D-120 清退移除）。
const (
	SrcSymdigAlias    = "symdig_alias"
	SrcSysretroWeekly = "sysretro-weekly"
	SrcSleepMoney     = "sleep_money"
	SrcExplorer       = "explorer" // D-513 既有写侧源
)

// Row 收件箱一行（status/resolution 由写侧固定 pending/空）。
type Row struct{ Source, Title, Reason, Action string }

var mu sync.Mutex

// Append 并发安全 O_APPEND 追加 v2 行（status=pending，resolution 空）。
// 旧五列表头/存量行不改——文件允许多表头段混存，分诊靠列数判定（向后兼容）。
func Append(path, date string, rows []Row) error {
	var block strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&block, "| %s | %s | %s | %s | %s | pending |  |\n",
			date, CleanCell(r.Source), CleanCell(r.Title), CleanCell(r.Reason), CleanCell(r.Action))
	}
	mu.Lock()
	defer mu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(block.String())
	return err
}

// CleanCell 落盘表格单元格清洗（同 kbreport cleanCell 口径）。
func CleanCell(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", "\\|")
}

// Backlog 未分诊行数：数据行 status 列==pending 或无 status 列（旧五列行=pending 语义）；
// accepted/rejected/done/wontfix 不计。跳表头/分隔行。
func Backlog(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	n := 0
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "|") || strings.Contains(ln, "---") ||
			strings.Contains(ln, "| 条目 |") || strings.Contains(ln, "| 候选 |") {
			continue
		}
		cells := strings.Split(ln, "|")
		fields := len(cells) - 2 // 首末空元
		if fields >= 7 {
			if strings.TrimSpace(cells[6]) == "pending" {
				n++
			}
		} else {
			n++ // 无 status 列旧行（列数不定）=pending 语义
		}
	}
	return n
}
