// classify.go 事件分类键提取（S2，纯函数，零推理）。
// 规则表写死：autotrade_logs.reason 首词组 / alerts.message 首段前缀。
package main

import "strings"

// classifyAutotradeLog 从 autotrade_logs.reason 提取分类键（S2.1）。
// 规则表写死：reason 首词组匹配已知前缀；未匹配 → 原文前 30 字符。
// 已知前缀（按 reason 实际形态归纳，写死不推理）：
//   - "daily loss" / "max positions" / "disabled" / "duplicate" / "balance" / "state"
//   - 修复⑭ S7 噪音映射：+ "leg notional imbalance" / "execute failed" / "quote stale" / "qty below"
//     （收口 per-qty/per-venue 数值噪音 → 单一分类键，待归纳.md 不再逐行膨胀）
func classifyAutotradeLog(reason string) string {
	r := strings.ToLower(strings.TrimSpace(reason))
	// 已知前缀表（首词组匹配，长前缀优先避免子串误命中：imbalance 含 balance）
	prefixes := []string{
		"leg notional imbalance", // S7：收口 buy $X vs sell $Y 数值噪音（须在 balance 前，imbalance 含 balance）
		"daily loss",
		"max positions",
		"disabled",
		"duplicate",
		"balance",
		"state",
		"execute failed", // S7：收口 pipeline FAILED 收尾
		"quote stale",    // S7：收口 net benefit degraded 长句
		"below",          // S7：收口 qty below volume step / qty N below venue min lot（below 为公共词）
	}
	for _, p := range prefixes {
		if strings.Contains(r, p) {
			return p
		}
	}
	// 未匹配 → 原文前 30 字符（截断，去换行）
	r = strings.ReplaceAll(r, "\n", " ")
	if len([]rune(r)) > 30 {
		return string([]rune(r)[:30])
	}
	return r
}

// classifyAlert 从 alerts.message 提取分类键（S2.2）。
// 规则：首个「：」或「，」前的前缀；无分隔符 → 前 30 字符。
// 修复⑭ S7 噪音映射：LEGGED 告警含 position N / 持仓 #N → 归一为 "legged"（收口 per-position 噪音）。
// 返修包① T2：LEGGED 改返条目键 "cross-broker-cfd-basis-risk"（matchEntry 按索引 id 子串命中 →
// 证据行追加到该条目，不进待归纳；单腿暴露=跨 broker 基差风险已知坑）。
func classifyAlert(message string) string {
	m := strings.TrimSpace(message)
	// S7 噪音映射：LEGGED 告警归一（position N LEGGED / 持仓 #N LEGGED → cross-broker-cfd-basis-risk 条目键）
	ml := strings.ToLower(m)
	if strings.Contains(ml, "legged") {
		return "cross-broker-cfd-basis-risk"
	}
	// 找首个中文逗号/英文逗号/中文冒号/英文冒号
	seps := []string{"：", ":", "，", ","}
	minIdx := len(m)
	for _, s := range seps {
		if idx := strings.Index(m, s); idx >= 0 && idx < minIdx {
			minIdx = idx
		}
	}
	if minIdx < len(m) {
		prefix := strings.TrimSpace(m[:minIdx])
		if prefix != "" {
			return strings.ToLower(prefix)
		}
	}
	// 无分隔符 → 前 30 字符
	r := strings.ToLower(m)
	r = strings.ReplaceAll(r, "\n", " ")
	if len([]rune(r)) > 30 {
		return string([]rune(r)[:30])
	}
	return r
}

// matchEntry 分类键与索引条目匹配（S2.3）。
// grep 索引.md（id/kind/keywords 行）子串匹配（分类键小写化）。
// 命中多个取第一命中并记 debug（返回 matched=true + entryID）。
// entries 为预解析的索引行（id + keywords 拼成的可搜索串）。
func matchEntry(classKey string, entries []indexRow) (entryID string, matched bool) {
	ck := strings.ToLower(classKey)
	for _, e := range entries {
		// 搜索串 = id + keywords（小写）
		hay := strings.ToLower(e.id + " " + e.keywords)
		if strings.Contains(hay, ck) || strings.Contains(ck, strings.ToLower(e.id)) {
			return e.id, true
		}
	}
	return "", false
}

// ignoreKeys 返修包① T2：风控正常触发类=系统按设计工作，零信号，不进待归纳不写条目。
// - "max positions"：持仓达上限拒单（D-058 守卫正常工作）
// - "daily loss"：日亏达限 halt（D-058 守卫正常工作）
// - "state"：system state=DAILY_LOSS_HALT (not RUNNING) 洪水（475 行/日 append 进 server-facts 条目）
// 抽小函数保单测可达。
var ignoreKeys = map[string]bool{
	"max positions": true,
	"daily loss":    true,
	"state":         true,
}

// isIgnored 分类键是否在忽略表（风控正常触发类，零信号不进待归纳）。
func isIgnored(classKey string) bool {
	return ignoreKeys[strings.ToLower(strings.TrimSpace(classKey))]
}

// indexRow 索引.md 表行解析结果（id + keywords，用于匹配）。
type indexRow struct {
	id       string
	kind     string
	keywords string
}
