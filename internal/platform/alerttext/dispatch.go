// dispatch.go —— 席位转发开工令组装（D-622 泛化面）。与归一面同包：消费方同为
// pgstore 告警漏斗与各直推旁路，叶包零项目依赖约束不变。
package alerttext

import "fmt"

// SeatDispatchLine D-622：需席位动作的告警/请求推送末行固定嵌可转发开工令（与复审收口
// 转交词同构——固定末位=缺席立辨，业主复制粘贴即派发；仅提示词载体，系统不代执行、
// 不做可点击执行）。首件=selfwatch LIVE 时效告警；泛化=pgstore fin_condition_gap
// 请求箱推送（D-622 泛化面票）。注意：行内含数字段会被 AlertDedupKey 归一——
// 请求 id 不参与去重判异，同文案异 id 窗内仍互抑（与原键语义一致）。
func SeatDispatchLine(role, goal string) string {
	return fmt.Sprintf("\n[角色:%s] %s", role, goal)
}
