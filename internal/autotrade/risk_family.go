// D-256-A S2：CARRY/SWAPARB 风险族合并淘汰。
// CARRY 与 SWAPARB 共享同一底层风险（跨 broker swap 息差），分桶统计使各自样本量
// 低于 5 笔淘汰阈值 → 永不触发。riskFamily 把两者映射到同一族 []string，
// 调用方用族聚合（AggregateStrategyStatsByEnvironmentFamily）合并样本。
package autotrade

// riskFamily 返回策略所属风险族的策略名列表（用于族级聚合）。
// CARRY ↔ SWAPARB 互为同族（共享底层风险），其他策略自成一族。
// 返回切片（非 nil）便于直接传 ANY($1) 查询；调用方不修改返回值。
func riskFamily(strategy string) []string {
	switch strategy {
	case "CARRY", "SWAPARB":
		return []string{"CARRY", "SWAPARB"}
	default:
		return []string{strategy}
	}
}

// FamilyResolverAdapter 把 riskFamily 包装成 settlement.FamilyResolver（D-256-A S2）。
// 供 main 装配时注入 settlement.Settle 的 family 参数。
// 签名同 riskFamily（func(string) []string），settlement.FamilyResolver 类型隐式匹配。
func FamilyResolverAdapter(strategy string) []string {
	return riskFamily(strategy)
}
