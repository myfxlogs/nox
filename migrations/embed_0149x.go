package migrations

import _ "embed"

// 0149-0162 段自 embed.go 拆出（D-006 同规：行数拆分）。
var (
	//go:embed replay/0149_settlement_incident_attributed.sql
	SettlementIncidentAttributedSQL string

	//go:embed replay/0150_explorer_shell.sql
	ExplorerShellSQL string

	//go:embed replay/0151_config_to_pg.sql
	ConfigToPgSQL string

	//go:embed replay/0152_macro_tables.sql
	MacroTablesSQL string

	//go:embed replay/0153_radar_watchlist_canonical.sql
	RadarWatchlistCanonicalSQL string

	//go:embed replay/0154_calib_tables.sql
	CalibTablesSQL string
	//go:embed replay/0155_idle_floor_streak.sql
	IdleFloorStreakSQL string
	//go:embed replay/0156_exchange_configs_seed_endpoint_fossil.sql
	EndpointFossilSeedSQL string // D-543 补登：691544b2 只落文件未注册（从未应用）
	//go:embed replay/0157_exposure_caps_seed.sql
	ExposureCapsSeedSQL string
	//go:embed replay/0159_exposure_cap_truth_fix.sql
	ExposureCapTruthFixSQL string // D-547：组合帽对齐+okx 幽灵键重键名
	//go:embed replay/0160_paper_dual_cex_columns.sql
	PaperDualCexColumnsSQL string // D-555 S1：paper_spot_carry/carry_graduations venue_type+venue_key 列
	//go:embed replay/0161_quote_unit_scales.sql
	QuoteUnitScalesSQL string // D-594：broker_symbol_map price_scale/qty_scale + 铜行定标 + XCU 别名
	//go:embed replay/0162_position_fees_netpnl.sql
	PositionFeesNetPnlSQL string // D-599：positions fees_usd/fee_quality 新列（realized_pnl 改净额）
)
