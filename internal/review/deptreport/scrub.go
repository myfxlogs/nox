// scrub.go —— LLM 入文/出文脱敏（1301 实证 2026-10-13）：上游网关内容安全闸
// 对「币安行情」CJK 短语硬触发、「冻结」与具体资金金额并置软触发（资金冻结
// 诈骗语境误伤），单方各自均放行。只洗喂给 LLM 的文本与落库前正文——DB 原值
// 不动。新 1301 实证出现新触发词时按同径补映射。
package deptreport

import "strings"

// scrubRepl 触发词映射（实证驱动，不臆测扩词）。
var scrubRepl = strings.NewReplacer(
	"币安", "binance", // 「币安行情」整词硬触发；英文 binance 各面畅行
	"冻结", "停滞", // 「冻结」+金额并置软触发；「停滞」语义等价零实损
)

// scrub 脱敏一处文本（部报输入段/manager 拼接段/落库正文同口径）。
func scrub(s string) string {
	return scrubRepl.Replace(s)
}

// mgrBodyBudget manager 输入单部正文预算（runes）——2026-10-13 实证：十部
// 全文 ~4.5KB 同源告警簇复述越 1301 判分线（重试 4 连挂/行 dedup 仍挂），
// 300 行界截断两档+头尾档三验放行。
const mgrBodyBudget = 300

// compact 头 2/3+尾 1/3 行界缩量：部报正文超预算时保①员工状态段与③异常/
// 待办段（经理摘要最有用两端），中段以「…」占位。仅用于 manager 输入拼装，
// 落库正文保持全文。
func compact(body string, max int) string {
	lines := strings.Split(body, "\n")
	total := 0
	lens := make([]int, len(lines))
	for i, ln := range lines {
		lens[i] = len([]rune(ln))
		total += lens[i]
	}
	if total <= max {
		return body
	}
	kept := make([]bool, len(lines))
	head, n := max*2/3, 0
	for i := range lines {
		if n+lens[i] > head {
			break
		}
		kept[i] = true
		n += lens[i]
	}
	tail, n := max/3, 0
	for i := len(lines) - 1; i >= 0; i-- {
		if kept[i] || n+lens[i] > tail {
			break
		}
		kept[i] = true
		n += lens[i]
	}
	var b strings.Builder
	cut := false
	for i, ln := range lines {
		if kept[i] {
			b.WriteString(ln + "\n")
		} else if !cut {
			b.WriteString("…\n")
			cut = true
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
