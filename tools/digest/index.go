// index.go 索引.md 解析（S2.3，独立实现，不 import noxkb）。
// 解析索引表行 → indexRow 列表，供 matchEntry 子串匹配。
package main

import (
	"strings"
)

// parseIndex 解析经验库索引 markdown 表行 → indexRow 列表。
// 表格式：| id | kind | keywords | summary | status | risk |
// 跳过非表行（标题、分隔行 |---|、引用 >）。
func parseIndex(md string) []indexRow {
	lines := strings.Split(md, "\n")
	rows := make([]indexRow, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "|-") || strings.HasPrefix(line, "| id") {
			continue
		}
		inner := strings.Trim(line, "|")
		fields := strings.Split(inner, "|")
		if len(fields) < 6 {
			continue
		}
		rows = append(rows, indexRow{
			id:       strings.TrimSpace(fields[0]),
			kind:     strings.TrimSpace(fields[1]),
			keywords: strings.TrimSpace(fields[2]),
		})
	}
	return rows
}
