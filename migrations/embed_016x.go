package migrations

import _ "embed"

var (
	//go:embed 0163_strategy_stats_notify.sql
	StrategyStatsNotifySQL string // D-004 P1#3：strategy_stats.disabled 变更 → NOTIFY（替代 60s 轮询）
	//go:embed 0164_canonical_exposure_caps.sql
	CanonicalExposureCapsSQL string // D-006 S3：fin_canonical_exposure_caps 聚簇敞口闸
	//go:embed 0165_mt5_exposure_cap_6000.sql
	MT5ExposureCap6000SQL string // D-007 S2：MT5 两户 venue 敞口闸 $6000（业主批）
	//go:embed 0166_margin_semantics.sql
	MarginSemanticsSQL string // D-010：margin_usd 列+互斥约束；venue cap 语义翻保证金
)
