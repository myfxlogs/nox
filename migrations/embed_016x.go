package migrations

import _ "embed"

var (
	//go:embed 0163_strategy_stats_notify.sql
	StrategyStatsNotifySQL string // D-004 P1#3：strategy_stats.disabled 变更 → NOTIFY（替代 60s 轮询）
	//go:embed 0164_canonical_exposure_caps.sql
	CanonicalExposureCapsSQL string // D-006 S3：fin_canonical_exposure_caps 聚簇敞口闸
)
