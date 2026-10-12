// deps.go —— D-422：settleDeps 桥接（pgstore.Store → settlement.Deps），
// 复制 cmd/settleonce 模式（独立 cmd 不可 import 其他 main 包，先例即复制）。
package main

import (
	"context"
	"time"

	"arb/internal/settlement"
	"arb/internal/store/pgstore"
)

// settleDeps 把 pgstore.Store 适配为 settlement.Deps（复制 cmd/settleonce 桥接——
// 独立 cmd 不可 import 其他 main 包，先例即复制）。
type settleDeps struct {
	store *pgstore.Store
}

var _ settlement.Deps = (*settleDeps)(nil)

func (d *settleDeps) GetPosition(ctx context.Context, id int64) (*pgstore.Position, error) {
	return d.store.GetPosition(ctx, id)
}
func (d *settleDeps) ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error) {
	return d.store.ListPositionLegs(ctx, posID)
}
func (d *settleDeps) ContractSizeByCanonical(ctx context.Context, venue, canonical string) (float64, bool, error) {
	return d.store.ContractSizeByCanonical(ctx, venue, canonical)
}
func (d *settleDeps) USDPer(ctx context.Context, quoteCcy string, at time.Time) (float64, bool) {
	return d.store.USDPer(ctx, quoteCcy, at)
}
func (d *settleDeps) UpsertVerificationSample(ctx context.Context, vkey string, expectedUsd, realizedUsd float64, closedAt time.Time) error {
	return d.store.UpsertVerificationSample(ctx, vkey, expectedUsd, realizedUsd, closedAt)
}
func (d *settleDeps) UpdateStrategyStat(ctx context.Context, strategy string, realizedPnl float64) error {
	return d.store.UpdateStrategyStat(ctx, strategy, realizedPnl)
}
func (d *settleDeps) GetStrategyStat(ctx context.Context, strategy string) (*pgstore.StrategyStat, error) {
	return d.store.GetStrategyStat(ctx, strategy)
}
func (d *settleDeps) AggregateStrategyStats(ctx context.Context, strategy string) (pgstore.AggregateStat, error) {
	return d.store.AggregateStrategyStats(ctx, strategy)
}
func (d *settleDeps) AggregateStrategyStatsByEnvironment(ctx context.Context, strategy, environment string) (pgstore.AggregateStat, error) {
	return d.store.AggregateStrategyStatsByEnvironment(ctx, strategy, environment)
}
func (d *settleDeps) AggregateStrategyStatsByEnvironmentFamily(ctx context.Context, strategies []string, environment string) (pgstore.AggregateStat, error) {
	return d.store.AggregateStrategyStatsByEnvironmentFamily(ctx, strategies, environment)
}
func (d *settleDeps) DisableStrategy(ctx context.Context, strategy, reason string) error {
	return d.store.DisableStrategy(ctx, strategy, reason)
}

func (d *settleDeps) AppConfigFloat(ctx context.Context, key string) (float64, bool, error) {
	return d.store.AppConfigFloat(ctx, key)
}
func (d *settleDeps) InsertAlert(ctx context.Context, ruleID *int64, level, message string) error {
	return d.store.InsertAlert(ctx, ruleID, level, message)
}
func (d *settleDeps) InsertSettlement(ctx context.Context, r *pgstore.SettlementRow) (bool, error) {
	return d.store.InsertSettlement(ctx, r)
}
func (d *settleDeps) HasSettlement(ctx context.Context, posID int64) (bool, error) {
	return d.store.HasSettlement(ctx, posID)
}
func (d *settleDeps) SumCashflowsNet(ctx context.Context, posID int64) (float64, error) {
	return d.store.SumCashflowsNet(ctx, posID)
}
func (d *settleDeps) SumCashflowsNetWithQuality(ctx context.Context, posID int64) (float64, string, error) {
	return d.store.SumCashflowsNetWithQuality(ctx, posID)
}
func (d *settleDeps) SumCashflowsNetForSettle(ctx context.Context, posID int64, hasFundingLeg, fundingDue bool) (float64, string, error) {
	return d.store.SumCashflowsNetForSettle(ctx, posID, hasFundingLeg, fundingDue)
}
func (d *settleDeps) GetSettlementStatsStatus(ctx context.Context, posID int64) (string, error) {
	return d.store.GetSettlementStatsStatus(ctx, posID)
}
func (d *settleDeps) MarkSettlementStatsRetryable(ctx context.Context, posID int64) error {
	return d.store.MarkSettlementStatsRetryable(ctx, posID)
}

// D-475a S7：预测-实测对照三委托。
func (d *settleDeps) InsertFlag(ctx context.Context, f *pgstore.VenueSymbolFlag) error {
	return d.store.InsertFlag(ctx, f)
}
func (d *settleDeps) ListActiveFlags(ctx context.Context) (map[string]pgstore.VenueSymbolFlag, error) {
	return d.store.ListActiveFlags(ctx)
}
func (d *settleDeps) LatestPaperByLegs(ctx context.Context, canonical string, legVenues []string) (*pgstore.PaperSpotCarry, error) {
	return d.store.LatestPaperByLegs(ctx, canonical, legVenues)
}
