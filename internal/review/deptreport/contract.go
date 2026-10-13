// contract.go —— D-249④ 日报 SOP 输出契约（MetaGPT 式 schema 校验，只管结构不管数字）：
// money-first 固定序八段+末行「明日关注」为必段；生成后 Go 校验，
// 缺段/乱序→带补正提示重生成一次；仍缺→fail-open 落库+「契约缺段」标记+log。
package deptreport

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// contractSection 必段定义：name=违约标记名，probes=正文须含任一关键词（取首个命中位）。
var contractSections = []struct {
	name   string
	probes []string
}{
	{"①NAV 与闲置", []string{"NAV"}},
	{"②在仓与 carry", []string{"在仓", "carry"}},
	{"③待决提案", []string{"提案"}},
	{"④请求箱", []string{"请求箱"}},
	{"⑤策略态变", []string{"策略"}},
	{"⑥事故与硬线", []string{"事故", "硬线"}},
	{"⑦流健康", []string{"流健康", "数据流"}},
	{"⑧昨日待办→今日状态", []string{"待办", "昨日"}},
	{"末行明日关注", []string{"明日关注"}},
}

// contractMiss 校验正文结构：返回缺段/乱序段名（空=契约满足）。
// 序段须按固定序出现（money-first），乱序段视同违约记入名单。
func contractMiss(body string) []string {
	var miss []string
	prev := -1
	for _, s := range contractSections {
		idx := -1
		for _, p := range s.probes {
			if i := strings.Index(body, p); i >= 0 && (idx < 0 || i < idx) {
				idx = i
			}
		}
		if idx < 0 || idx <= prev {
			miss = append(miss, s.name)
			continue // 缺席/乱序均记违；prev 不推进，防连环误判
		}
		prev = idx
	}
	return miss
}

// contractFixNote 补正提示：并入二次生成的 user 输入尾部。
func contractFixNote(miss []string) string {
	return fmt.Sprintf("\n【契约补正】上次输出缺段/乱序：%s。请按固定序八段+末行「明日关注」重出全文，禁缺段禁乱序。\n", strings.Join(miss, "、"))
}

// contractBackfill 缺段兜底（视窗 C 件）：补正重生成仍缺的序段，把 gather
// 已产出的该段原料确定拼接回正文——宁带原料段不挂空标记；拼接只限 gather
// 已产段（【N…】头圈码对名），禁编造；「末行明日关注」无原料段只留 marker。
func contractBackfill(body, input string, miss []string) string {
	for _, m := range miss {
		r, _ := utf8.DecodeRuneInString(m)
		if r < '①' || r > '⑧' {
			continue
		}
		if raw := rawSection(input, string(r)); raw != "" {
			body += "\n\n【契约补段·原料 " + m + "】\n" + raw
		}
	}
	return body
}

// rawSection 取 gather 输入中【N…】头段原文（至下一【头或文末；无=空）。
func rawSection(input, num string) string {
	hdr := "【" + num
	i := strings.Index(input, hdr)
	if i < 0 {
		return ""
	}
	rest := input[i:]
	if j := strings.Index(rest[len(hdr):], "\n【"); j >= 0 {
		rest = rest[:len(hdr)+j]
	}
	return strings.TrimSpace(rest)
}
