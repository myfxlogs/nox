// d249_s4_upgrade_test.go —— D-249 S4 批3 第2项：settlement 升级路径（retryable→applied）。
// 审计 P1-2：InsertSettlement ON CONFLICT DO NOTHING → 首写 retryable 后重试永远 won=false 静默返回，
// 无 retryable→applied 升级路径；verification 样本与策略统计静默丢失。
// 修法：ON CONFLICT DO UPDATE SET ... WHERE stats_status<>'applied'（或显式 Upsert），让重试升级。
package settlement

import (
	"context"
	"testing"
	"time"

	"arb/internal/platform/testutil"
	"arb/internal/store/pgstore"
)

// testStore settlement 包 D-249 S4 新增：DB 测试用 store（复用 pgstore.Migrate）。
func testStore(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

// TestD249_Settle_RetryableUpgrade_FakeDeps 验证 Settle() 流逻辑：retryable 行重试时升级为 applied 并走统计更新。
// R-G4：原名 TestD249_Settle_RetryableUpgrade 与 TestD249_InsertSettlement_RetryableUpgrade_RealDB 易混淆——
// 本测试用 fakeDeps（insertWon 手填=true），验证 Settle() 流程编排（短路/升级/统计调用计数），
// 非 DB 升级路径证明；真证明见 TestD249_InsertSettlement_RetryableUpgrade_RealDB（RealDB ON CONFLICT 实测）。
func TestD249_Settle_RetryableUpgrade_FakeDeps(t *testing.T) {
	now := time.Now()
	rp := 10.0
	// 模拟：首写 retryable（statsStatus=retryable 短路不触发，继续 Settle），
	// InsertSettlement 模拟 ON CONFLICT 升级（won=true，重试赢家）。
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:             1,
			OpportunityID:  "CROSS_VENUE:AUDJPY:52993526:mt5:277842155:mt5",
			Type:           "CROSS_VENUE",
			Status:         "CLOSED",
			ExpectedNetBps: 15.0,
			RealizedPnl:    &rp,
			OpenedAt:       &now,
			ClosedAt:       &now,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:          100000,
		csOk:        true,
		fxRate:      150,
		fxOk:        true,
		statsStatus: "retryable", // 短路不触发（非 applied/skipped），继续 Settle
		insertWon:   true,        // 模拟 ON CONFLICT 升级成功（won=true）
	}
	if err := Settle(context.Background(), d, 1, noElim, nil); err != nil {
		t.Fatalf("Settle retryable upgrade: %v", err)
	}
	if d.insertSettlement != 1 {
		t.Errorf("insertSettlement=%d, want 1（retryable 行被升级重写）", d.insertSettlement)
	}
	if d.upsertVSample != 1 {
		t.Errorf("upsertVSample=%d, want 1（升级为 applied 后应走统计更新）", d.upsertVSample)
	}
	if d.updateStat != 1 {
		t.Errorf("updateStat=%d, want 1（升级为 applied 后应更新策略统计）", d.updateStat)
	}
}

// TestD249_Settle_AppliedNotOverwritten 验证 applied 行不被重写（WHERE stats_status<>'applied' 守卫）。
// 场景：已有 applied 行（statsStatus=applied）→ 短路返回，不调 InsertSettlement。
func TestD249_Settle_AppliedNotOverwritten(t *testing.T) {
	d := &fakeDeps{statsStatus: "applied"}
	if err := Settle(context.Background(), d, 100, noElim, nil); err != nil {
		t.Fatalf("Settle applied: %v", err)
	}
	if d.insertSettlement != 0 {
		t.Errorf("insertSettlement=%d, want 0（applied 短路不重写）", d.insertSettlement)
	}
}

// TestD249_Settle_SkippedNotOverwritten 验证 skipped 行不被重写。
func TestD249_Settle_SkippedNotOverwritten(t *testing.T) {
	d := &fakeDeps{statsStatus: "skipped"}
	if err := Settle(context.Background(), d, 100, noElim, nil); err != nil {
		t.Fatalf("Settle skipped: %v", err)
	}
	if d.insertSettlement != 0 {
		t.Errorf("insertSettlement=%d, want 0（skipped 短路不重写）", d.insertSettlement)
	}
}

// TestD249_InsertSettlement_RetryableUpgrade_RealDB 验证 pgstore.InsertSettlement 真实 DB 升级路径。
// 首写 retryable → 重试 applied → 行被升级（stats_status 从 retryable 变 applied，won=true）。
func TestD249_InsertSettlement_RetryableUpgrade_RealDB(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE position_settlements, positions RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	posID, err := s.CreatePosition(ctx, &pgstore.Position{OpportunityID: "test:retry-up", Type: "SWAPARB", Status: "CLOSED"})
	if err != nil {
		t.Fatalf("seed position: %v", err)
	}
	mv := 1
	// 首写 retryable
	row1 := &pgstore.SettlementRow{
		PositionID: posID, Vkey: "test:vkey", ExpectedUsd: 100, SettledAt: time.Now(),
		PnlQuality: "unknown", CompletenessStatus: "unknown", StatsStatus: "retryable",
		SettlementModelVersion: &mv, SettlementEventKey: "settle:5001",
	}
	won1, err := s.InsertSettlement(ctx, row1)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if !won1 {
		t.Fatal("first insert won=false, want true（首写应成功）")
	}
	// 重试 applied（升级）
	row2 := &pgstore.SettlementRow{
		PositionID: posID, Vkey: "test:vkey", ExpectedUsd: 100, RealizedUsd: 10,
		NotionalUsd: ptrFloat(1000), SettledAt: time.Now(),
		PricePnlUsd: ptrFloat(5), CashflowsNetUsd: ptrFloat(5), NetRealizedUsd: ptrFloat(10),
		PnlQuality: "actual", CompletenessStatus: "complete", StatsStatus: "applied",
		SettlementModelVersion: &mv, SettlementEventKey: "settle:5001",
	}
	won2, err := s.InsertSettlement(ctx, row2)
	if err != nil {
		t.Fatalf("upgrade insert: %v", err)
	}
	if !won2 {
		t.Error("upgrade insert won=false, want true（retryable→applied 升级应成功）")
	}
	// 验证行已升级为 applied
	var statsStatus string
	if err := s.Pool().QueryRow(ctx, `SELECT stats_status FROM position_settlements WHERE position_id=$1`, posID).Scan(&statsStatus); err != nil {
		t.Fatalf("query: %v", err)
	}
	if statsStatus != "applied" {
		t.Errorf("stats_status=%q, want applied（升级后应为 applied）", statsStatus)
	}
}

// TestD249_InsertSettlement_AppliedNotOverwritten_RealDB 验证 applied 行不被重写（won=false）。
func TestD249_InsertSettlement_AppliedNotOverwritten_RealDB(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE position_settlements, positions RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	posID, err := s.CreatePosition(ctx, &pgstore.Position{OpportunityID: "test:applied-no", Type: "SWAPARB", Status: "CLOSED"})
	if err != nil {
		t.Fatalf("seed position: %v", err)
	}
	mv := 1
	// 首写 applied
	row1 := &pgstore.SettlementRow{
		PositionID: posID, Vkey: "test:vkey", ExpectedUsd: 100, RealizedUsd: 10,
		SettledAt: time.Now(), PnlQuality: "actual", CompletenessStatus: "complete",
		StatsStatus: "applied", SettlementModelVersion: &mv, SettlementEventKey: "settle:5002",
	}
	won1, err := s.InsertSettlement(ctx, row1)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if !won1 {
		t.Fatal("first insert won=false, want true")
	}
	// 重试 applied（不应重写）
	row2 := &pgstore.SettlementRow{
		PositionID: posID, Vkey: "test:vkey", ExpectedUsd: 200, RealizedUsd: 20,
		SettledAt: time.Now(), PnlQuality: "actual", CompletenessStatus: "complete",
		StatsStatus: "applied", SettlementModelVersion: &mv, SettlementEventKey: "settle:5002",
	}
	won2, err := s.InsertSettlement(ctx, row2)
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if won2 {
		t.Error("second insert won=true, want false（applied 不应被重写）")
	}
	// 验证行保持首写值
	var expectedUsd float64
	if err := s.Pool().QueryRow(ctx, `SELECT expected_usd FROM position_settlements WHERE position_id=$1`, posID).Scan(&expectedUsd); err != nil {
		t.Fatalf("query: %v", err)
	}
	if expectedUsd != 100 {
		t.Errorf("expected_usd=%.2f, want 100（applied 不应被重写）", expectedUsd)
	}
}
