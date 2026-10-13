// contract.go —— D-249④ 日报 SOP 输出契约（MetaGPT 式 schema 校验，只管结构不管数字）：
// money-first 固定序八段+末行「明日关注」为必段；生成后 Go 校验，
// 缺段/乱序→带补正提示重生成一次；仍缺→fail-open 落库+「契约缺段」标记+log。
package deptreport

import (
	"fmt"
	"strings"
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
