// employee_dept.go —— D-246 v3 §4.4：运行面部门标签收七值（六资金域+平台横切）。
package pgstore

// 部门标签七值（fin_employee_registry.dept 列口径）。平台横切非部门、无 KPI/决策权，
// 仅作运行面归类桶。旧标签映射由迁移 1006 落地：运营→研究、复盘→账本、
// IR/运维/合规→平台。
const (
	DeptData     = "数据"
	DeptResearch = "研究"
	DeptInvest   = "投资"
	DeptTrade    = "交易"
	DeptRisk     = "风控"
	DeptLedger   = "账本"
	DeptPlatform = "平台"
)

// EmployeeDepts 七标签全集（fin_employee_registry.dept 合法值域）。
var EmployeeDepts = []string{
	DeptData, DeptResearch, DeptInvest, DeptTrade, DeptRisk, DeptLedger, DeptPlatform,
}
