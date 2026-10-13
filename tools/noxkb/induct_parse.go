// induct_parse.go —— 待归纳源行解析 + inducted:<hash> 幂等标打。
// 待归纳.md：事件表恒为文件第一个表（同 digest D-357 口径），✅/inducted: 行=已消解跳过。
// 待归纳-分析员.md：D-172「归 … 域」行属人工复核域不升 KB，跳过不打标；其余行归纳后末列打 inducted: 标。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// pendingRow 待归纳源行（两文件统一的归纳单元）。
type pendingRow struct {
	File    string // 源文件路径
	Line    int    // 源行号（打标定位用）
	Date    string
	Source  string // autotrade_logs / alerts / analyst-inbox
	Summary string
}

// rowHash 行内容指纹（date|source|summary sha256 前 8 hex）——幂等标载体。
func rowHash(r pendingRow) string {
	sum := sha256.Sum256([]byte(r.Date + "|" + r.Source + "|" + r.Summary))
	return hex.EncodeToString(sum[:])[:8]
}

// parsePendingFile 解析待归纳.md 事件表：返回未消解行（无 ✅ 无 inducted:）。
func parsePendingFile(path string) ([]pendingRow, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	var rows []pendingRow
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "## ") {
			break // 首个段标题=事件区结束
		}
		if !strings.HasPrefix(t, "|") || strings.HasPrefix(t, "|-") || strings.HasPrefix(t, "| 日期") {
			continue
		}
		if strings.Contains(t, "✅") || strings.Contains(t, "inducted:") {
			continue
		}
		fields := strings.Split(strings.Trim(t, "|"), "|")
		if len(fields) < 4 {
			continue
		}
		rows = append(rows, pendingRow{
			File: path, Line: i,
			Date:    strings.TrimSpace(fields[0]),
			Source:  strings.TrimSpace(fields[1]),
			Summary: strings.TrimSpace(fields[2]),
		})
	}
	return rows, lines, nil
}

// parseInboxFile 解析待归纳-分析员.md 表行：
// 末列含「复核域」（D-172 域外）或 inducted: 的行跳过；其余返回待归纳。
func parseInboxFile(path string) ([]pendingRow, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	var rows []pendingRow
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "|") || strings.HasPrefix(t, "|-") || strings.HasPrefix(t, "| 日期") {
			continue
		}
		if strings.Contains(t, "inducted:") || strings.Contains(t, "复核域") {
			continue
		}
		fields := strings.Split(strings.Trim(t, "|"), "|")
		if len(fields) < 4 {
			continue
		}
		rows = append(rows, pendingRow{
			File: path, Line: i,
			Date:    strings.TrimSpace(fields[0]),
			Source:  "analyst-inbox",
			Summary: strings.TrimSpace(fields[1]) + " — " + strings.TrimSpace(fields[2]),
		})
	}
	return rows, lines, nil
}

// markRows 把已归纳行的 inducted:<hash> <tag> 标打进源行末列（行级原位改写）。
// 待归纳.md 行打 `✅ inducted:<h> <tag>`（复用 digest ✅ 消解出口，次跑自动归档）；
// 分析员 inbox 行打 `inducted:<h> <tag>`（该文件无 ✅ 归档机制，靠标幂等）。
// lines 为源文件按行切片（含行尾前的原始内容），写回整文件。
func markRows(lines []string, marks map[int]string) []string {
	out := make([]string, len(lines))
	copy(out, lines)
	for ln, tag := range marks {
		l := out[ln]
		idx := strings.LastIndex(l, "|")
		if idx < 0 {
			continue
		}
		out[ln] = strings.TrimRight(l[:idx], " ") + tag + " |"
	}
	return out
}
