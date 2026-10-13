package migrations

import _ "embed"

//go:embed replay/0200_dci_settle.sql
var DCISettleSQL string // D-207：DCI 交割观察（settled 态+settle_* 四列）

//go:embed replay/0201_leg_forensics.sql
var LegForensicsSQL string // D-208：position_legs.fail_reason+半平裸奔阈值种子

//go:embed 1001_employee_dept_eleven.sql
var EmployeeDeptElevenSQL string // D-220/D-225⑤：dept 八标签→十一部（采集/链上→数据、执行→交易、通知→IR）

//go:embed 1002_env_keys_nox.sql
var EnvKeysNoxSQL string // D-230：app_config/app_secrets 键名 ARB_→NOX_（改名原子票）

//go:embed 1003_drop_unmigrated.sql
var DropUnmigratedSQL string // M3 前置①：不搬表族 DROP（§3 裁定 12 表；切流先 pg_dump 归档）

//go:embed 1004_nox_ro_role.sql
var NoxRoRoleSQL string // D-241⑧：鲜装 nox_ro 补造（0041+0047 语义合并；生产切流走 ALTER RENAME）

//go:embed 1005_dept_reports.sql
var DeptReportsSQL string // D-244：fin_dept_reports（十一部部报+账本专项+经理摘要）

//go:embed 1006_m40_runtime_requestbox.sql
var M40RuntimeRequestboxSQL string // D-246/D-247 M4.0：台账降噪（未登记/已剔族清行+dept 七值）+请求箱 kind 白名单+runtime 转票

//go:embed 1007_llm_call.sql
var LLMCallSQL string // D-246 v3 §4.4：fin_llm_call LLM 调用账本（费用入 D-013 口径）

//go:embed 1008_opp_eval.sql
var OppEvalSQL string // D-258 M4.1：fin_opp_eval 评估卡（一提案一卡）

//go:embed 1009_strategy_lifecycle.sql
var StrategyLifecycleSQL string // D-260 M4.2：fin_strategy 状态机+fin_strategy_criteria 判据表+提案 SLA 键

//go:embed 1010_nav_budget.sql
var NavBudgetSQL string // D-264 M4.4：fin_nav_daily 日切 NAV + fin_strategy_budget 策略预算表

//go:embed 1011_venue_reject.sql
var VenueRejectSQL string // D-255：fin_venue_reject 败因坑表

//go:embed 1012_elim_hearing.sql
var ElimHearingSQL string // D-249①：fin_strategy_hearing 淘汰多空听证记录
