// budget_check.go —— D-262 M4.3④：预算一致性闸接口预留。
// fin_strategy_budget 表 M4.4 才建——本票只定接口签名+默认 fail-closed 实现，
// Gate.Submit 检点已接线（checker=nil 时跳过=未消费，零行为变化）；
// M4.4 装配 BudgetChecker 后同点生效，缺席预算行由实现侧判拒。
package gate

import "context"

// BudgetChecker 预算一致性检：策略名义 vs fin_strategy_budget 配额。
// 实现侧义务：预算行缺席/读败→拒（fail-closed），放行须显式预算锚。
type BudgetChecker interface {
	CheckBudget(ctx context.Context, strategy string, notionalUSD float64) (allow bool, reason string)
}

// FailClosedBudget 默认桩：恒拒。未装配真实现前若被误挂=全单拒（安全侧）。
type FailClosedBudget struct{}

func (FailClosedBudget) CheckBudget(context.Context, string, float64) (bool, string) {
	return false, "预算一致性闸未实现（fail-closed）"
}

// SetBudgetChecker 装配预算闸（M4.4 消费点；nil=不检查）。
func (g *Gate) SetBudgetChecker(c BudgetChecker) { g.budgetCheck = c }
