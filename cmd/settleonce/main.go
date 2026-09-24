// cmd/settleonce D-193-A 扩包返修 R3：settlement 补链微工具。
// 对指定 position 调 settlement.SettleAt（幂等，stats_status 短路），产出真实 settlement 行。
// 复用 settle_deps 桥接模式（settlement.Deps 接口实现），elim=autotrade.EliminatorAdapter。
//
// D-193-A 扩包返修 R3（ZCode 裁定）：479 补走本工具，禁手工 INSERT（方式 B 已否决）。
//
// 用法（复审方执行）：
//
//	ARB_PG_DSN="postgres://arb:arb@arb-postgres:5432/arb?sslmode=disable" \
//	  go run ./cmd/settleonce -pos=479
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"arb/internal/autotrade"
	"arb/internal/settlement"
	"arb/internal/store/pgstore"
)

// settleDeps 把 pgstore.Store 适配为 settlement.Deps（复制 cmd/core/settle_deps.go 桥接）。
// 独立 cmd 不依赖 cmd/core 包（main 包不可 import），故复制桥接。
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

func main() {
	posID := flag.Int64("pos", 0, "待结算 position ID（必填）")
	flag.Parse()
	if *posID <= 0 {
		log.Fatal("-pos 必填（position ID）")
	}
	dsn := os.Getenv("ARB_PG_DSN")
	if dsn == "" {
		log.Fatal("ARB_PG_DSN 未设置")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		log.Fatalf("pgstore.New: %v", err)
	}
	defer s.Close()

	deps := &settleDeps{store: s}

	// 结算前状态
	before, _ := s.GetSettlementStatsStatus(ctx, *posID)
	fmt.Printf("=== D-193-A 扩包 R3：settleonce pos=%d ===\n", *posID)
	fmt.Printf("before: stats_status=%q\n", before)

	// 调 settlement.SettleAt（幂等，elim=autotrade.EliminatorAdapter，family=autotrade.FamilyResolverAdapter）
	err = settlement.SettleAt(ctx, deps, *posID, autotrade.EliminatorAdapter, autotrade.FamilyResolverAdapter, time.Now())
	if err != nil {
		log.Fatalf("SettleAt pos=%d: %v", *posID, err)
	}

	// 结算后状态
	after, _ := s.GetSettlementStatsStatus(ctx, *posID)
	has, _ := s.HasSettlement(ctx, *posID)
	fmt.Printf("after:  stats_status=%q has_settlement=%v\n", after, has)
	fmt.Println("DONE（质量如实落库，幂等短路=applied/skipped 直接返回）")
}
