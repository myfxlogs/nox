package migrations

import _ "embed"

var (
	//go:embed replay/0170_broker_swap_free.sql
	BrokerSwapFreeSQL string // D-014 S1：broker_accounts.swap_free 旗标列 + 3001157 置 true + exchange_configs.taker_fee_bps

	//go:embed replay/0171_maker_fee_enable.sql
	MakerFeeEnableSQL string // D-018 后段：exchange_configs.maker_fee_bps + app_config maker 启用键

	//go:embed replay/0172_incident_exit_kind_cleanup.sql
	IncidentExitKindCleanupSQL string // D-032 R1：清零成交 FAILED 误打的 legged_unwind（#503/#504 形态）

	//go:embed replay/0173_paper_window_start.sql
	PaperWindowStartSQL string // D-033①：app_config 播种 ARB_PAPER_WINDOW_START（窗起点单源）

	//go:embed replay/0174_drop_opportunities_tick.sql
	DropOpportunitiesTickSQL string // D-040②：opportunities_tick 死表 DROP（stale 8-22 零引用，已备份）

	//go:embed replay/0175_probe_positions.sql
	ProbePositionsSQL string // D-047①：positions +decision_ref/auto_close_at + 到期索引

	//go:embed replay/0176_probe_llm_loop.sql
	ProbeLLMLoopSQL string // D-048①②：explorer_proposals 探针提案契约+审批流转列

	//go:embed replay/0177_macro_zh_reset.sql
	MacroZhResetSQL string // D-053①：en 旧行清除（zh 端点切换防日报双语重复）

	//go:embed replay/0178_drop_perp_funding_admission.sql
	DropPerpFundingAdmissionSQL string // D-057⑤：PERP_FUNDING 证据准入表退役（族停用，87 行已备份）

	//go:embed replay/0179_retire_universe_alerts.sql
	RetireUniverseAlertsSQL string // D-072：universe 发现告警退役——清历史 info 行

	//go:embed replay/0180_d120_retire_research.sql
	D120RetireResearchSQL string // D-120：研究面 A 批 13 表幂等 DROP（已备份）

	//go:embed replay/0181_facts_null_symbol_idx.sql
	FactsNullSymbolIdxSQL string // D-121：facts NULL-symbol 组枚举部分索引（0180 撞号 D-120，复审顺延 0181）
)
