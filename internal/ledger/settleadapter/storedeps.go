// Package settleadapter — pgstore.Store → settlement.Deps 共享桥接（D-508 S13：
// cmd/core settle_deps.go 与 cmd/settleonce 复制件收敛单源）。
package settleadapter

import (
	"context"
	"time"

	"arb/internal/ledger/settlement"
	"arb/internal/portfolio/lifecycle"
	"arb/internal/store/pgstore"
	"arb/internal/trading/exec"
)

// StoreDeps 把 pgstore.Store 适配为 settlement.Deps（D-087 §3.2）。
func New(store *pgstore.Store) *StoreDeps { return &StoreDeps{store: store} }

// pgstore.Store 已实现全部所需方法（GetPosition/ListPositionLegs/ContractSizeByCanonical/
// USDPer/UpsertVerificationSample/UpdateStrategyStat/GetStrategyStat/AggregateStrategyStats/
// AggregateStrategyStatsByEnvironment/DisableStrategy/InsertSettlement/HasSettlement/
// SumCashflowsNet/SumCashflowsNetWithQuality/GetSettlementStatsStatus/MarkSettlementStatsRetryable），本结构仅做接口声明桥接。
type StoreDeps struct {
	store      *pgstore.Store
	postSettle func(context.Context, int64) // D-032 ①：结算后钩子（autotrade 风控线注入）
}

// 编译期断言：settleStoreDeps 实现 settlement.Deps。
var _ settlement.Deps = (*StoreDeps)(nil)

// SetPostSettleHook D-032 ①：注入结算后回调（autotrade.SetSettleDeps 装配点调）。
func (d *StoreDeps) SetPostSettleHook(fn func(context.Context, int64)) { d.postSettle = fn }

// PostSettled 实现 settlement.PostSettler——未注入钩子时空操作。
func (d *StoreDeps) PostSettled(ctx context.Context, posID int64) {
	if d.postSettle != nil {
		d.postSettle(ctx, posID)
	}
}

func (d *StoreDeps) GetPosition(ctx context.Context, id int64) (*pgstore.Position, error) {
	return d.store.GetPosition(ctx, id)
}

func (d *StoreDeps) ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error) {
	return d.store.ListPositionLegs(ctx, posID)
}

func (d *StoreDeps) ContractSizeByCanonical(ctx context.Context, venue, canonical string) (float64, bool, error) {
	return d.store.ContractSizeByCanonical(ctx, venue, canonical)
}

func (d *StoreDeps) USDPer(ctx context.Context, quoteCcy string, at time.Time) (float64, bool) {
	return d.store.USDPer(ctx, quoteCcy, at)
}

func (d *StoreDeps) UpsertVerificationSample(ctx context.Context, vkey string, expectedUsd, realizedUsd float64, closedAt time.Time) error {
	return d.store.UpsertVerificationSample(ctx, vkey, expectedUsd, realizedUsd, closedAt)
}

func (d *StoreDeps) UpdateStrategyStat(ctx context.Context, strategy string, realizedPnl float64) error {
	return d.store.UpdateStrategyStat(ctx, strategy, realizedPnl)
}

func (d *StoreDeps) GetStrategyStat(ctx context.Context, strategy string) (*pgstore.StrategyStat, error) {
	return d.store.GetStrategyStat(ctx, strategy)
}

func (d *StoreDeps) AggregateStrategyStats(ctx context.Context, strategy string) (pgstore.AggregateStat, error) {
	return d.store.AggregateStrategyStats(ctx, strategy)
}

// 按 environment 过滤的聚合桥接（pgstore.Store 已实现）。
func (d *StoreDeps) AggregateStrategyStatsByEnvironment(ctx context.Context, strategy, environment string) (pgstore.AggregateStat, error) {
	return d.store.AggregateStrategyStatsByEnvironment(ctx, strategy, environment)
}

// 族级聚合桥接（pgstore.Store 已实现）。
func (d *StoreDeps) AggregateStrategyStatsByEnvironmentFamily(ctx context.Context, strategies []string, environment string) (pgstore.AggregateStat, error) {
	return d.store.AggregateStrategyStatsByEnvironmentFamily(ctx, strategies, environment)
}

func (d *StoreDeps) DisableStrategy(ctx context.Context, strategy, reason string) error {
	return d.store.DisableStrategy(ctx, strategy, reason)
}

func (d *StoreDeps) AppConfigFloat(ctx context.Context, key string) (float64, bool, error) {
	return d.store.AppConfigFloat(ctx, key)
}

func (d *StoreDeps) InsertAlert(ctx context.Context, ruleID *int64, level, message string) error {
	return d.store.InsertAlert(ctx, ruleID, level, message)
}

func (d *StoreDeps) InsertSettlement(ctx context.Context, r *pgstore.SettlementRow) (bool, error) {
	return d.store.InsertSettlement(ctx, r)
}

func (d *StoreDeps) HasSettlement(ctx context.Context, posID int64) (bool, error) {
	return d.store.HasSettlement(ctx, posID)
}

func (d *StoreDeps) SumCashflowsNet(ctx context.Context, posID int64) (float64, error) {
	return d.store.SumCashflowsNet(ctx, posID)
}

// 返回 (sum, quality) 供 completeness 判定。
func (d *StoreDeps) SumCashflowsNetWithQuality(ctx context.Context, posID int64) (float64, string, error) {
	return d.store.SumCashflowsNetWithQuality(ctx, posID)
}

// 结算专用净额委托。
func (d *StoreDeps) SumCashflowsNetForSettle(ctx context.Context, posID int64, hasFundingLeg, fundingDue bool) (float64, string, error) {
	return d.store.SumCashflowsNetForSettle(ctx, posID, hasFundingLeg, fundingDue)
}

// 查 stats_status 用于短路判定。
func (d *StoreDeps) GetSettlementStatsStatus(ctx context.Context, posID int64) (string, error) {
	return d.store.GetSettlementStatsStatus(ctx, posID)
}

// 统计更新失败时改 stats_status=retryable。
func (d *StoreDeps) MarkSettlementStatsRetryable(ctx context.Context, posID int64) error {
	return d.store.MarkSettlementStatsRetryable(ctx, posID)
}

// Settle 适配 dashboard.Settler / reconcile.Settler / exec.Settler 接口（D-087 §3.2 三调用点 + D-193-A 扩包 S5 pipeline 路径共用）。
// elim 用 lifecycle.EliminatorAdapter，family 用 lifecycle.FamilyResolverAdapter（main 装配时注入）。
func (d *StoreDeps) Settle(ctx context.Context, posID int64) error {
	return settlement.Settle(ctx, d, posID, lifecycle.EliminatorAdapter, lifecycle.FamilyResolverAdapter)
}

// compile-time: settleStoreDeps 实现 exec.Settler（D-193-A 扩包 S5）。
var _ exec.Settler = (*StoreDeps)(nil)

// 预测-实测对照三委托（写旗/查旗/纸面行回查）。
func (d *StoreDeps) InsertFlag(ctx context.Context, f *pgstore.VenueSymbolFlag) error {
	return d.store.InsertFlag(ctx, f)
}
func (d *StoreDeps) ListActiveFlags(ctx context.Context) (map[string]pgstore.VenueSymbolFlag, error) {
	return d.store.ListActiveFlags(ctx)
}
func (d *StoreDeps) LatestPaperByLegs(ctx context.Context, canonical string, legVenues []string) (*pgstore.PaperSpotCarry, error) {
	return d.store.LatestPaperByLegs(ctx, canonical, legVenues)
}
