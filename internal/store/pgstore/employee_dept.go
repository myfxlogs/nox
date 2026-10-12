// employee_dept.go —— D-220 十一部编制枚举（roster 装配改写归 M2.6）。
package pgstore

// 部门标签十一值（fin_employee_registry.dept 列口径）。
const (
	DeptData     = "数据" // 采集/链上并入
	DeptResearch = "研究"
	DeptInvest   = "投资"
	DeptTrade    = "交易" // 旧「执行」
	DeptRisk     = "风控"
	DeptOps      = "运营"
	DeptLedger   = "账本"
	DeptReview   = "复盘"
	DeptIR       = "IR" // 旧「通知」
	DeptInfra    = "运维"
	DeptComp     = "合规"
)

// EmployeeDepts 十一部全集（迁移 1001 后 fin_employee_registry.dept 合法值域）。
var EmployeeDepts = []string{
	DeptData, DeptResearch, DeptInvest, DeptTrade, DeptRisk, DeptOps,
	DeptLedger, DeptReview, DeptIR, DeptInfra, DeptComp,
}
