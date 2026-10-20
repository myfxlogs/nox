// settlement_deps.go —— settlement 注入面类型（自 settlement.go 整搬，纯搬家零语义——保 ≤300 行门禁）。
package settlement

import (
	"context"
	"time"

	"nox/internal/store/pgstore"
)

// Deps 结算依赖（注入面，调用方装配）。
type Deps interface {
	// GetPosition 读单条持仓（含 typed 字段）。
	GetPosition(ctx context.Context, id int64) (*pgstore.Position, error)
	// ListPositionLegs 读持仓腿。
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
	// ContractSizeByCanonical 查 CS（已含 VenueAccount 归一）。
	ContractSizeByCanonical(ctx context.Context, venue, canonical string) (float64, bool, error)
	// USDPer 查计价币→USD 汇率（FXRateProvider）。
	USDPer(ctx context.Context, quoteCcy string, at time.Time, preferVenueKey string) (float64, bool)
	// UpsertVerificationSample 增量验证统计。
	UpsertVerificationSample(ctx context.Context, vkey string, expectedUsd, realizedUsd float64, closedAt time.Time) error
	// UpdateStrategyStat 策略统计增量。
	UpdateStrategyStat(ctx context.Context, strategy string, realizedPnl float64) error
	// GetStrategyStat 读策略统计（D-154 起仅查 disabled/disabled_reason；累计列不再用于淘汰判定）。
	GetStrategyStat(ctx context.Context, strategy string) (*pgstore.StrategyStat, error)
	// AggregateStrategyStats 实时聚合策略全量平仓净值（D-154 S1 淘汰判定 v2 真相源）。
	AggregateStrategyStats(ctx context.Context, strategy string) (pgstore.AggregateStat, error)
	// AggregateStrategyStatsByEnvironment D-200：按 environment 过滤的聚合（environment='' 等价于原行为）。
	AggregateStrategyStatsByEnvironment(ctx context.Context, strategy, environment string) (pgstore.AggregateStat, error)
	// AggregateStrategyStatsByEnvironmentFamily D-256-A S2：风险族聚合（多策略合并样本，p.type=ANY($1)）。
	// CARRY/SWAPARB 同族合并统计，避免分桶样本量不足永不触发淘汰。
	AggregateStrategyStatsByEnvironmentFamily(ctx context.Context, strategies []string, environment string) (pgstore.AggregateStat, error)
	// DisableStrategy 禁用策略（淘汰）。
	DisableStrategy(ctx context.Context, strategy, reason string) error
	// AppConfigFloat D-180：淘汰证据 Zσ 热读（app_config NOX_ELIM_ZSIGMA，种子径）。
	AppConfigFloat(ctx context.Context, key string) (float64, bool, error)
	// InsertAlert 写告警（D-268 告警点 4：结算时策略淘汰告警）。
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
	// InsertSettlement 写 position_settlements（幂等 ON CONFLICT DO NOTHING，返修包②返回 won）。
	InsertSettlement(ctx context.Context, s *pgstore.SettlementRow) (won bool, err error)
	// HasSettlement 查 position_settlements 是否已有行（幂等短路）。
	HasSettlement(ctx context.Context, posID int64) (bool, error)
	// SumCashflowsNet 返回持仓 swap+funding+commission 现金流 USD 合计（D-127 P4 返修①：net=realized+cashflowsNet）。
	SumCashflowsNet(ctx context.Context, posID int64) (float64, error)
	// 替代 SumCashflowsNet，返回 quality 供 completeness 判定。
	SumCashflowsNetWithQuality(ctx context.Context, posID int64) (sum float64, quality string, err error)
	// 结算专用净额——funding 实收以 income_events 为准替换公式投影轨。
	SumCashflowsNetForSettle(ctx context.Context, posID int64, hasFundingLeg, fundingDue bool) (sum float64, quality string, err error)
	// 审计 P0-1：查 settlement 行的 stats_status，用于短路判定。
	// 不存在行 → 返回 ("", nil)（继续走 Settle 流程）。
	// 存在行 → 返回 stats_status（applied/skipped 短路；pending/retryable 继续）。
	GetSettlementStatsStatus(ctx context.Context, posID int64) (statsStatus string, err error)
	// 审计 P0-2：统计更新失败时改 stats_status=retryable，下轮重试。
	MarkSettlementStatsRetryable(ctx context.Context, posID int64) error
	// 预测-实测对照——warn flag 自动写+同键查重+纸面行回查。
	InsertFlag(ctx context.Context, f *pgstore.VenueSymbolFlag) error
	ListActiveFlags(ctx context.Context) (map[string]pgstore.VenueSymbolFlag, error)
	// LatestPaperByLegs canonical+两腿裸名回查最近纸面行（venue 组合串含两腿裸名匹配）。
	LatestPaperByLegs(ctx context.Context, canonical string, legVenues []string) (*pgstore.PaperSpotCarry, error)
}

// Eliminator 淘汰判定函数类型（从 autotrade.evaluateElimination 抽出，避免环依赖）。
// D-260 M4.2：加 ctx——判据实时读表（fin_strategy_criteria 改行即生效）。
// 返回 (shouldDisable, reason)。
type Eliminator func(ctx context.Context, totalTrades int, winRate, avgPnl, medianPnl float64, disabled bool) (bool, string)

// ElimHearer D-249①：淘汰多空听证观察钩（可选接口——Deps 未实现=不听证）。
// 裁定击杀前同步调用；听证仅观察面不翻裁定（LLM 对辩文本落 fin_strategy_hearing）。
type ElimHearer interface {
	HearElimination(ctx context.Context, trigger string, fam []string,
		total int, winRate, avgPnl, medianPnl float64, reason string)
}

// FamilyResolver D-256-A S2：策略→风险族映射（避免环依赖，autotrade.riskFamily 注入）。
// 返回同族策略名列表（如 CARRY→[CARRY,SWAPARB]），用于族级聚合。
// nil = 无族合并（每策略独立聚合，向后兼容）。
type FamilyResolver func(strategy string) []string
