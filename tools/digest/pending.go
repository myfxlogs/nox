// pending.go 待归纳.md 历史解析 + 已消解行归档（S3.2/消解出口）——自 write.go 拆出（D-309 纯移动，零行为变更）。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// resolvedEvent 已消解事件（「次数」列尾 ✅ 标记行，digest 自动移入归档——消解出口）。
type resolvedEvent struct {
	Date    string // 首现日期 YYYY-MM-DD
	Source  string // autotrade_logs / alerts
	Summary string // 事件摘要
	Count   int    // 聚合计数
	Verdict string // ✅ 后的分诊结论
}

// parsePendingHistory 从现有待归纳.md 解析历史事件行（S3.2 跨日聚合用）。
// 解析表格行 | 日期 | 来源 | 事件摘要 | 次数 ⚠ 重复 ✅ 结论 |：
// 「次数」列不含 ✅ → active（继续跨日聚合）；含 ✅ → resolved（已消解，不进活跃聚合，digest 移入归档）。
// 文件不存在或无表行 → 返回双空（首次运行）。
func parsePendingHistory(path string) ([]pendingEvent, []resolvedEvent) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil // 文件不存在 = 首次运行
	}
	lines := strings.Split(string(data), "\n")
	var active []pendingEvent
	var resolved []resolvedEvent
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// D-357：事件表恒为文件第一个表——首个 `## ` 段标题即离开事件区，
		// 段内表行（spot-carry/费率轨/覆盖率段等）不再作事件重放（表行均以 | 开头，无误判）。
		if strings.HasPrefix(line, "## ") {
			break
		}
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "|-") || strings.HasPrefix(line, "| 日期") {
			continue
		}
		inner := strings.Trim(line, "|")
		fields := strings.Split(inner, "|")
		if len(fields) < 4 {
			continue
		}
		date := strings.TrimSpace(fields[0])
		source := strings.TrimSpace(fields[1])
		summary := strings.TrimSpace(fields[2])
		countStr := strings.TrimSpace(fields[3])
		if mark := strings.Index(countStr, "✅"); mark >= 0 {
			verdict := strings.TrimSpace(countStr[mark+len("✅"):])
			countPart := strings.TrimSuffix(strings.TrimSpace(countStr[:mark]), " ⚠ 重复")
			var count int
			fmt.Sscanf(strings.TrimSpace(countPart), "%d", &count)
			if count == 0 {
				count = 1
			}
			resolved = append(resolved, resolvedEvent{
				Date:    date,
				Source:  source,
				Summary: summary,
				Count:   count,
				Verdict: verdict,
			})
			continue
		}
		// 去掉 ⚠ 重复 标记后解析次数
		countStr = strings.TrimSuffix(countStr, " ⚠ 重复")
		countStr = strings.TrimSpace(countStr)
		var count int
		fmt.Sscanf(countStr, "%d", &count)
		if count == 0 {
			count = 1
		}
		active = append(active, pendingEvent{
			Date:     date,
			Source:   source,
			ClassKey: strings.ToLower(summary), // 用 summary 作 classKey（历史行无独立 classKey 列）
			Summary:  summary,
			Count:    count,
		})
	}
	return active, resolved
}

// appendResolvedArchive 把已消解行追加到 docs/经验库/归档/待归纳-YYYY-MM.md（消解出口）。
// 行签名（日期+来源+摘要+次数）已在归档文件出现则跳过——归档追加与待归纳覆写两步间崩溃重跑不双写。
func appendResolvedArchive(kbDir string, resolved []resolvedEvent, now time.Time) error {
	if len(resolved) == 0 {
		return nil
	}
	archiveDir := filepath.Join(kbDir, "归档")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		return fmt.Errorf("mkdir archive: %w", err)
	}
	path := filepath.Join(archiveDir, "待归纳-"+now.Format("2006-01")+".md")
	data, _ := os.ReadFile(path) // 不存在 = 新建
	content := string(data)
	var buf strings.Builder
	if !strings.Contains(content, "# 待归纳归档") {
		buf.WriteString("# 待归纳归档（digest 自动生成：待归纳.md「次数」列尾 ✅ 标记的已消解行）\n\n")
		buf.WriteString("| 消解日 | 日期 | 来源 | 事件摘要 | 次数 | 分诊结论 |\n")
		buf.WriteString("|--------|------|------|----------|------|----------|\n")
	}
	resolveDate := now.Format("2006-01-02")
	written := map[string]bool{}
	appended := 0
	for _, r := range resolved {
		sig := fmt.Sprintf("| %s | %s | %s | %d |", r.Date, r.Source, r.Summary, r.Count)
		if strings.Contains(content, sig) || written[sig] {
			continue
		}
		written[sig] = true
		buf.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %d | %s |\n", resolveDate, r.Date, r.Source, r.Summary, r.Count, r.Verdict))
		appended++
	}
	if appended == 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(buf.String()); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}
	return nil
}
