// write.go 条目正文追加重现行 + 待归纳.md 覆写 + staleness（S3）。
// 严格按方案 §2：正文「## 证据」节末追加；无证据节跳过不发明；待归纳聚合含 ⚠ 重复标记。
package main

import (
	"fmt"
	"strings"
	"time"
)

// appendEvidenceLine 在条目正文「## 证据」节末追加一行重现证据（S3.1）。
// 格式：`- YYYY-MM-DD 重现: <摘要≤60字> | ref: <表>#<id>`
// 正文无「## 证据」节 → 返回原文 + ok=false（跳过该条目并入待归纳，不发明结构）。
// 「## 证据」节末 = 下一节 `## ` 标题前 或 文件末尾。
func appendEvidenceLine(content, date, summary, ref string) (string, bool) {
	// 定位「## 证据」节
	evidenceIdx := strings.Index(content, "\n## 证据\n")
	if evidenceIdx < 0 {
		// 兼容文件首行就是 ## 证据（无前置换行）
		if strings.HasPrefix(content, "## 证据\n") {
			evidenceIdx = -1 // 后续 +1 处理
		} else {
			return content, false // 无证据节，跳过不发明
		}
	}
	sectionStart := evidenceIdx + 1 // 跳过前置换行
	// 找下一节 `## ` 标题（证据节之后）
	rest := content[sectionStart+len("## 证据\n"):]
	nextSectionIdx := strings.Index(rest, "\n## ")
	var insertPos int
	if nextSectionIdx >= 0 {
		insertPos = sectionStart + len("## 证据\n") + nextSectionIdx
	} else {
		// 证据节是最后一节 → 追加到文件末尾
		insertPos = len(content)
		// 确保末尾有换行
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
			insertPos = len(content)
		}
	}
	// 摘要截断 ≤60 字
	summary = truncateSummary(summary, 60)
	line := fmt.Sprintf("- %s 重现: %s | ref: %s\n", date, summary, ref)
	return content[:insertPos] + line + content[insertPos:], true
}

// truncateSummary 截断摘要到 maxRunes 字符（去换行，超长加 …）。
func truncateSummary(s string, maxRunes int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

// pendingEvent 待归纳事件（S3.2 聚合单元）。
type pendingEvent struct {
	Date     string // YYYY-MM-DD
	Source   string // autotrade_logs / alerts
	ClassKey string // 分类键
	Summary  string // 事件摘要
	Count    int    // 聚合计数
}

// renderPendingMd 按方案模板整体覆写待归纳.md（S3.2）。
// ⚠ 重复标记 = 同分类键跨日出现 ≥2 次（从清单历史累计，文件本身即状态）。
// events 为本次新增事件；historyEvents 为从现有待归纳.md 解析的历史事件（含跨日计数）。
// staleness 为 staleness 报告条目（S3.3）。
func renderPendingMd(events []pendingEvent, historyEvents []pendingEvent, staleness []stalenessRow) string {
	var buf strings.Builder
	buf.WriteString("# 待归纳事件（digest 自动生成，勿手编）\n")
	buf.WriteString("> 未匹配任何既有条目的异常事件——会话 Claude 开工时优先消化；同类事件跨会话重复出现 = LLM 接入触发证据。\n")
	buf.WriteString("> 消解出口：分诊完成的行在「次数」列尾追加 ` ✅ 结论`（如 `12 ✅ 噪音判否（D#）`，与 ⚠ 标记可共存），digest 次跑自动移入 归档/待归纳-<YYYY-MM>.md；除此外勿手编。\n\n")
	// 聚合：同 classKey 跨日计数（历史 + 本次）
	agg := aggregatePending(append(historyEvents, events...))
	// 表格
	buf.WriteString("| 日期 | 来源 | 事件摘要 | 次数 |\n")
	buf.WriteString("|------|------|----------|------|\n")
	// 按次数降序、日期降序排列
	sortPendingByCountThenDate(agg)
	for _, e := range agg {
		mark := ""
		if e.Count >= 2 {
			mark = " ⚠ 重复"
		}
		buf.WriteString(fmt.Sprintf("| %s | %s | %s | %d%s |\n", e.Date, e.Source, e.Summary, e.Count, mark))
	}
	// staleness 报告节
	if len(staleness) > 0 {
		buf.WriteString("\n## staleness 报告\n\n")
		buf.WriteString("> evidence_refs 含 server_fact 且 last_verified 超 30 天的条目——需会话 Claude 复核是否仍成立。\n\n")
		buf.WriteString("| 条目 | last_verified | 距今天数 |\n")
		buf.WriteString("|------|---------------|----------|\n")
		for _, s := range staleness {
			buf.WriteString(fmt.Sprintf("| %s | %s | %d |\n", s.ID, s.LastVerified, s.DaysSince))
		}
	}
	return buf.String()
}

// pendingEventAgg 聚合后的事件（含跨日计数）。
type pendingEventAgg struct {
	Date     string
	Source   string
	ClassKey string
	Summary  string
	Count    int
}

// aggregatePending 聚合同 classKey 的事件（跨日计数）。
// 保留首次出现的 date/source/summary 作为代表行，Count 累加所有同 classKey 事件。
// 返修包① T3：历史行携带的 Count 不再丢弃——初值取 e.Count、累加 g.Count += e.Count
// （原 Count++/初值 1 把历史聚合计数清零，重跑 404→1 即此 bug）。
func aggregatePending(events []pendingEvent) []pendingEventAgg {
	groups := map[string]*pendingEventAgg{}
	var order []string
	for _, e := range events {
		if g, ok := groups[e.ClassKey]; ok {
			g.Count += e.Count
		} else {
			groups[e.ClassKey] = &pendingEventAgg{
				Date:     e.Date,
				Source:   e.Source,
				ClassKey: e.ClassKey,
				Summary:  e.Summary,
				Count:    e.Count,
			}
			order = append(order, e.ClassKey)
		}
	}
	out := make([]pendingEventAgg, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	return out
}

// sortPendingByCountThenDate 按次数降序、日期降序排列（原地）。
func sortPendingByCountThenDate(agg []pendingEventAgg) {
	// 简单插入排序（事件数少，无需 sort 包）
	for i := 1; i < len(agg); i++ {
		for j := i; j > 0; j-- {
			if agg[j].Count > agg[j-1].Count || (agg[j].Count == agg[j-1].Count && agg[j].Date > agg[j-1].Date) {
				agg[j], agg[j-1] = agg[j-1], agg[j]
			} else {
				break
			}
		}
	}
}

// stalenessRow staleness 报告行（S3.3）。
type stalenessRow struct {
	ID           string
	LastVerified string
	DaysSince    int
}

// parseStaleness 从条目 frontmatter 解析 staleness（S3.3）。
// evidence_refs 含 server_fact 且 last_verified 距今 >30 天 → stalenessRow。
// now 注入时钟（可单测）。
func parseStaleness(entries []frontmatterEntry, now time.Time) []stalenessRow {
	var out []stalenessRow
	cutoff := now.AddDate(0, 0, -30)
	for _, e := range entries {
		if !hasServerFact(e.EvidenceRefs) {
			continue
		}
		lv, err := time.Parse("2006-01-02", e.LastVerified)
		if err != nil {
			continue
		}
		days := int(now.Sub(lv).Hours() / 24)
		if lv.Before(cutoff) {
			out = append(out, stalenessRow{
				ID:           e.ID,
				LastVerified: e.LastVerified,
				DaysSince:    days,
			})
		}
	}
	return out
}

// hasServerFact 检查 evidence_refs 是否含 type: server_fact。
func hasServerFact(refs []map[string]interface{}) bool {
	for _, r := range refs {
		if t, ok := r["type"].(string); ok && t == "server_fact" {
			return true
		}
	}
	return false
}

// resolvedEvent/parsePendingHistory/appendResolvedArchive 已拆至 pending.go（D-309 纯移动）。
