package dashboard

import "fmt"

// fundingWarning 计算资金费翻负/急降预警（设计 §11.1，D-043 后端纯函数，前端只渲染）。
// current 为本日/本次利息、prev 为前一日利息（nil = 无前日）、dropRatio 为急降比例。
// 返回空串 = 正常。优先级 R1（翻负）> R2（急降）；首条无 prev 只走 R1。
func fundingWarning(current, prev *float64, dropRatio float64) string {
	if dropRatio <= 0 {
		dropRatio = 0.5
	}
	// R1：今日翻负——套息方向证伪，最严重，命中直接返回。
	if current != nil && *current < 0 {
		return fmt.Sprintf("资金费已翻负，套息变倒贴（今日 −%.2f U）", -(*current))
	}
	// R2：有 prev 且今日 ≥0 且 < 比例×prev（严格小于）才急降；prev≤0 自然免疫（今日≥0 不可能小于负比例积）。
	if current != nil && prev != nil && *current >= 0 && *prev > 0 && *current < dropRatio**prev {
		return fmt.Sprintf("资金费急降（前日 %.2f → 今日 %.2f U），接近翻负", *prev, *current)
	}
	// R3：其余无预警。
	return ""
}
