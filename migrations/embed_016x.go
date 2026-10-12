package migrations

import _ "embed"

var (
	//go:embed replay/0163_strategy_stats_notify.sql
	StrategyStatsNotifySQL string // D-004 P1#3：strategy_stats.disabled 变更 → NOTIFY（替代 60s 轮询）
	//go:embed replay/0164_canonical_exposure_caps.sql
	CanonicalExposureCapsSQL string // D-006 S3：fin_canonical_exposure_caps 聚簇敞口闸
	//go:embed replay/0165_mt5_exposure_cap_6000.sql
	MT5ExposureCap6000SQL string // D-007 S2：MT5 两户 venue 敞口闸 $6000（业主批）
	//go:embed replay/0166_margin_semantics.sql
	MarginSemanticsSQL string // D-010：margin_usd 列+互斥约束；venue cap 语义翻保证金
	//go:embed replay/0167_leg_external_status.sql
	LegExternalStatusSQL string // D-015 ②：position_legs.status 增 EXTERNAL（venue 推送平仓事件落「外部已平」标记）
	//go:embed replay/0168_d013_ledger_truth.sql
	D013LedgerTruthSQL string // D-013：partial 合法化 + #497–507 commission/exit_kind/净额回填
	//go:embed replay/0169_leg_fill_kind.sql
	LegFillKindSQL string // D-018 阶段a：position_legs.fill_kind（maker|taker，econ 成本修正输入；0169=0168 被 D-013 占用顺延）
)
