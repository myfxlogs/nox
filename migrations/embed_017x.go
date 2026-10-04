package migrations

import _ "embed"

var (
	//go:embed 0170_broker_swap_free.sql
	BrokerSwapFreeSQL string // D-014 S1：broker_accounts.swap_free 旗标列 + 3001157 置 true + exchange_configs.taker_fee_bps

	//go:embed 0171_maker_fee_enable.sql
	MakerFeeEnableSQL string // D-018 后段：exchange_configs.maker_fee_bps + app_config maker 启用键

	//go:embed 0172_incident_exit_kind_cleanup.sql
	IncidentExitKindCleanupSQL string // D-032 R1：清零成交 FAILED 误打的 legged_unwind（#503/#504 形态）

	//go:embed 0173_paper_window_start.sql
	PaperWindowStartSQL string // D-033①：app_config 播种 ARB_PAPER_WINDOW_START（窗起点单源）

	//go:embed 0174_drop_opportunities_tick.sql
	DropOpportunitiesTickSQL string // D-040②：opportunities_tick 死表 DROP（stale 8-22 零引用，已备份）

	//go:embed 0175_probe_positions.sql
	ProbePositionsSQL string // D-047①：positions +decision_ref/auto_close_at + 到期索引

	//go:embed 0176_probe_llm_loop.sql
	ProbeLLMLoopSQL string // D-048①②：explorer_proposals 探针提案契约+审批流转列

	//go:embed 0177_macro_zh_reset.sql
	MacroZhResetSQL string // D-053①：en 旧行清除（zh 端点切换防日报双语重复）
)
