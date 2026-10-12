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
