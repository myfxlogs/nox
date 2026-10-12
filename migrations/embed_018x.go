package migrations

import _ "embed"

var (
	//go:embed replay/0182_binance_earn_seed.sql
	BinanceEarnSeedSQL string // D-130 返修 A 案：binance:earn 凭据复制补种 + binance:funding 行摘除

	//go:embed replay/0183_drop_binance_earn.sql
	DropBinanceEarnSQL string // D-133：binance earn 面整体摘除（counter-migration 收口 0182 播种）
)

//go:embed replay/0184_drop_cnfi_repo.sql
var DropCnfiRepoSQL string // D-139：CNFI/逆回购面 13 表幂等 DROP（生产已备份）

//go:embed replay/0185_facts_kind_ts_idx.sql
var FactsKindTsIdxSQL string // D-152 fixup：facts(kind,ts)——retention 超窗候选范围寻址

//go:embed replay/0186_elim_evidence_seed.sql
var ElimEvidenceSeedSQL string // D-180：统计证据阈值 app_config 播种（Zσ/转折点标注线）

//go:embed replay/0187_cex_dci_quote.sql
var CexDCIQuoteSQL string // D-183：三所 DCI/双币产品报价表

//go:embed replay/0189_capability_constraint.sql
var CapabilityConstraintSQL string // D-186②：能力约束表（scope 四枚举+nullable position_id）

//go:embed replay/0190_condition_gap.sql
var ConditionGapSQL string // D-186③：条件缺口表（open→requested→fulfilled/waived 态机）

//go:embed replay/0191_capability_permission.sql
var CapabilityPermissionSQL string // D-188②：权限探测面（venue×permission×state 探活记录）

//go:embed replay/0192_dci_order.sql
var DCIOrderSQL string // D-191：DCI 申购执行轨幂等账本（request_id UNIQUE）

//go:embed replay/0193_employee_registry.sql
var EmployeeRegistrySQL string // D-190：员工花名册（daemon 编制+心跳+生死）
//go:embed replay/0188_opp_template.sql
var OppTemplateSQL string // D-185：机会模板册（fin_opp_template+cond 子表，首批 9 族）

//go:embed replay/0194_proposal.sql
var ProposalSQL string // D-189：提案实体化 fin_proposal+fin_dci_order.proposal_id 回链

//go:embed replay/0195_gap_resolve_note.sql
var GapResolveNoteSQL string // D-193②：缺口收口备注列

//go:embed replay/0196_otm_bound.sql
var OTMBoundSQL string // D-191-R9：%OTM 闸界入约束表

//go:embed replay/0197_dci_order_ref.sql
var DCIOrderRefSQL string // D-191-R10：报价行 order_ref 补列

//go:embed replay/0198_dci_amount_bound.sql
var DCIAmountBoundSQL string // D-191-R11：报价行 min/max/decimal 三字段

//go:embed replay/0199_dormant_deferred.sql
var DormantDeferredSQL string // D-204：deferred 态+休眠轨种子
