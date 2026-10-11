package migrations

import _ "embed"

var (
	//go:embed 0182_binance_earn_seed.sql
	BinanceEarnSeedSQL string // D-130 返修 A 案：binance:earn 凭据复制补种 + binance:funding 行摘除

	//go:embed 0183_drop_binance_earn.sql
	DropBinanceEarnSQL string // D-133：binance earn 面整体摘除（counter-migration 收口 0182 播种）
)

//go:embed 0184_drop_cnfi_repo.sql
var DropCnfiRepoSQL string // D-139：CNFI/逆回购面 13 表幂等 DROP（生产已备份）

//go:embed 0185_facts_kind_ts_idx.sql
var FactsKindTsIdxSQL string // D-152 fixup：facts(kind,ts)——retention 超窗候选范围寻址

//go:embed 0186_elim_evidence_seed.sql
var ElimEvidenceSeedSQL string // D-180：统计证据阈值 app_config 播种（Zσ/转折点标注线）

//go:embed 0187_cex_dci_quote.sql
var CexDCIQuoteSQL string // D-183：三所 DCI/双币产品报价表

//go:embed 0189_capability_constraint.sql
var CapabilityConstraintSQL string // D-186②：能力约束表（scope 四枚举+nullable position_id）

//go:embed 0190_condition_gap.sql
var ConditionGapSQL string // D-186③：条件缺口表（open→requested→fulfilled/waived 态机）
//go:embed 0188_opp_template.sql
var OppTemplateSQL string // D-185：机会模板册（fin_opp_template+cond 子表，首批 9 族）
