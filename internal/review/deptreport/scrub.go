// scrub.go —— LLM 入文/出文脱敏（1301 实证 2026-10-13）：上游网关内容安全闸
// 对「币安行情」CJK 短语硬触发、「冻结」与具体资金金额并置软触发（资金冻结
// 诈骗语境误伤），单方各自均放行。只洗喂给 LLM 的文本与落库前正文——DB 原值
// 不动。D-247③：出文追加投资人接触面泄漏拦截（「建议业主/业主面」类句整行剔
// 除——报告只到系统内，向业主的动作建议属越线措辞）。
package deptreport

import "strings"

// scrubRepl 触发词映射（实证驱动，不臆测扩词）。
var scrubRepl = strings.NewReplacer(
	"币安", "binance", // 「币安行情」整词硬触发；英文 binance 各面畅行
	"冻结", "停滞", // 「冻结」+金额并置软触发；「停滞」语义等价零实损
)

// scrub 脱敏一处文本（LLM 输入段同口径）。
func scrub(s string) string {
	return scrubRepl.Replace(s)
}

// ownerLeakPats D-247③ 投资人接触面泄漏词表（含其一的整行拦截剔除）。
var ownerLeakPats = []string{
	"建议业主", "业主面", "请示业主", "请业主", "建议投资人", "投资人应",
}

// scrubBody 落库前正文脱敏：触发词映射 + 业主面泄漏行整行剔除
// （剔行不整篇拒绝——单句泄漏不毁整报；剔除后正文留其余段落原值）。
func scrubBody(body string) string {
	body = scrub(body)
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		leak := false
		for _, p := range ownerLeakPats {
			if strings.Contains(ln, p) {
				leak = true
				break
			}
		}
		if !leak {
			out = append(out, ln)
		}
	}
	return strings.Join(out, "\n")
}
