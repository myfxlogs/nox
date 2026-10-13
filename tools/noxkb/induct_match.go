// induct_match.go —— 未消解行对拍既有条目关键词：命中=已有条目覆盖，不入新稿。
// 确定性规则（零 LLM）：事件摘要小写后对每条目计「命中关键词数」——
// ≥2 个关键词命中，或单个 ≥12 字符指纹级关键词命中 → covered:<id>（高置信才自动消解）。
// 单短关键词子串命中不足以定论（实拍误例：「binance」把 CROSS_VENUE 复审行误归 hmac 条目）。
// 多命中取命中数最多者，平取最长关键词所在条目；零命中留给 LLM 聚类段。
package main

import "strings"

// matchCovered 行摘要 vs 条目关键词对拍。返回命中条目 id；零命中返回空。
func matchCovered(summary string, entries []*Entry) string {
	s := strings.ToLower(summary)
	best, bestScore := "", 0
	for _, e := range entries {
		hits, longest := 0, 0
		for _, kw := range e.Keywords {
			kw = strings.ToLower(strings.TrimSpace(kw))
			if len([]rune(kw)) < 4 { // 过短关键词零区分度，不配对
				continue
			}
			if strings.Contains(s, kw) {
				hits++
				if n := len([]rune(kw)); n > longest {
					longest = n
				}
			}
		}
		if hits == 0 || (hits == 1 && longest < 12) {
			continue
		}
		if score := hits*1000 + longest; score > bestScore {
			best, bestScore = e.ID, score
		}
	}
	return best
}
