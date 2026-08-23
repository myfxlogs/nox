package settlement

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// fakeDeps 测试用 settlement.Deps 实现。
type fakeDeps struct {
	position         *pgstore.Position
	legs             []pgstore.PositionLeg
	cs               float64
	csOk             bool
	fxRate           float64
	fxOk             bool
	hasSettlement    bool
	insertSettlement int
	insertWon        bool // 返修包②：模拟 InsertSettlement 返回 won
	insertFirstOnly  bool // 返修包②：true=仅第一次 won，后续 false（并发模拟）
	upsertVSample    int
	updateStat       int
	getStat          *pgstore.StrategyStat
	disableStrategy  string
	mu               sync.Mutex // 并发安全计数
}

func (f *fakeDeps) GetPosition(ctx context.Context, id int64) (*pgstore.Position, error) {
	if f.position == nil {
		return nil, errors.New("not found")
	}
	return f.position, nil
}

func (f *fakeDeps) ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error) {
	return f.legs, nil
}

func (f *fakeDeps) ContractSizeByCanonical(ctx context.Context, venue, canonical string) (float64, bool, error) {
	return f.cs, f.csOk, nil
}

func (f *fakeDeps) USDPer(ctx context.Context, quoteCcy string, at time.Time) (float64, bool) {
	return f.fxRate, f.fxOk
}

func (f *fakeDeps) UpsertVerificationSample(ctx context.Context, vkey string, expectedUsd, realizedUsd float64, closedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertVSample++
	return nil
}

func (f *fakeDeps) UpdateStrategyStat(ctx context.Context, strategy string, realizedPnl float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateStat++
	return nil
}

func (f *fakeDeps) GetStrategyStat(ctx context.Context, strategy string) (*pgstore.StrategyStat, error) {
	if f.getStat == nil {
		return &pgstore.StrategyStat{Strategy: strategy}, nil
	}
	return f.getStat, nil
}

func (f *fakeDeps) DisableStrategy(ctx context.Context, strategy, reason string) error {
	f.disableStrategy = strategy
	return nil
}

func (f *fakeDeps) InsertSettlement(ctx context.Context, r *pgstore.SettlementRow) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.insertSettlement++
	if f.insertFirstOnly {
		return f.insertSettlement == 1, nil // 并发模拟：仅第一次 won
	}
	return f.insertWon, nil
}

func (f *fakeDeps) HasSettlement(ctx context.Context, posID int64) (bool, error) {
	return f.hasSettlement, nil
}

// noElim 永不禁用的 Eliminator（测试用）。
func noElim(int, float64, float64, bool) (bool, string) { return false, "" }

// TestSettle_Idempotent 已有 position_settlements 行 → 短路，不重复结算。
func TestSettle_Idempotent(t *testing.T) {
	d := &fakeDeps{hasSettlement: true}
	if err := Settle(context.Background(), d, 100, noElim); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.insertSettlement != 0 || d.upsertVSample != 0 || d.updateStat != 0 {
		t.Errorf("幂等短路失败：insert=%d upsert=%d updateStat=%d", d.insertSettlement, d.upsertVSample, d.updateStat)
	}
}

// TestSettle_HappyPath CLOSED + realized + expectedUsd>0 → 写 settlement + upsert + updateStat。
func TestSettle_HappyPath(t *testing.T) {
	now := time.Now()
	rp := 10.0
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
		cs:        100000,
		csOk:      true,
		fxRate:    150,
		fxOk:      true,
		insertWon: true, // 返修包②：模拟赢家
	}
	if err := Settle(context.Background(), d, 1, noElim); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.insertSettlement != 1 {
		t.Errorf("insertSettlement=%d, want 1", d.insertSettlement)
	}
	if d.upsertVSample != 1 {
		t.Errorf("upsertVSample=%d, want 1", d.upsertVSample)
	}
	if d.updateStat != 1 {
		t.Errorf("updateStat=%d, want 1", d.updateStat)
	}
}

// TestSettle_SkipRealizedNULL realized_pnl NULL → 写 settlement 但 skipped，不入样本。
func TestSettle_SkipRealizedNULL(t *testing.T) {
	now := time.Now()
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:             2,
			OpportunityID:  "CROSS_VENUE:AUDJPY:52993526:mt5:277842155:mt5",
			Type:           "CROSS_VENUE",
			Status:         "CLOSED",
			ExpectedNetBps: 15.0,
			RealizedPnl:    nil,
			OpenedAt:       &now,
			ClosedAt:       &now,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs:        100000,
		csOk:      true,
		fxRate:    150,
		fxOk:      true,
		insertWon: true, // 返修包②：模拟赢家（走 skipped 路径非并发输家）
	}
	if err := Settle(context.Background(), d, 2, noElim); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.insertSettlement != 1 {
		t.Errorf("insertSettlement=%d, want 1（跳过也写行）", d.insertSettlement)
	}
	if d.upsertVSample != 0 || d.updateStat != 0 {
		t.Errorf("跳过不应入样本：upsert=%d updateStat=%d", d.upsertVSample, d.updateStat)
	}
}

// TestSettle_SkipFxUnavailable fx 不可得 → skipped，不入样本。
func TestSettle_SkipFxUnavailable(t *testing.T) {
	now := time.Now()
	rp := 10.0
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:             3,
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
		cs:        100000,
		csOk:      true,
		fxRate:    0,
		fxOk:      false, // JPY 汇率不可得
		insertWon: true,  // 返修包②：模拟赢家（走 skipped 路径非并发输家）
	}
	if err := Settle(context.Background(), d, 3, noElim); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.insertSettlement != 1 {
		t.Errorf("insertSettlement=%d, want 1", d.insertSettlement)
	}
	if d.upsertVSample != 0 {
		t.Errorf("fx 不可得应跳过样本：upsert=%d", d.upsertVSample)
	}
}

// TestSettle_EliminatorTriggered 淘汰判定禁用 → DisableStrategy 被调。
func TestSettle_EliminatorTriggered(t *testing.T) {
	now := time.Now()
	rp := -5.0
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:             4,
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
		cs:        100000,
		csOk:      true,
		fxRate:    150,
		fxOk:      true,
		insertWon: true, // 返修包②：模拟赢家
		getStat:   &pgstore.StrategyStat{Strategy: "CROSS_VENUE", TotalTrades: 10, WinRate: 0.1, AvgPnl: -10},
	}
	alwaysElim := func(int, float64, float64, bool) (bool, string) { return true, "test elimination" }
	if err := Settle(context.Background(), d, 4, alwaysElim); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.disableStrategy != "CROSS_VENUE" {
		t.Errorf("disableStrategy=%q, want CROSS_VENUE", d.disableStrategy)
	}
}

func ptrFloat(v float64) *float64 { return &v }

// TestSettle_ConcurrentLoser 返修包②：并发双计窗口防护。
// 两个 goroutine 同 posID 并发 Settle——HasSettlement 短路都通过后，
// InsertSettlement 只有一个 won=true（赢家），另一个 won=false（输家）→ return nil 不走步骤 7/8。
// 断言：upsertVSample 只 +1（不是 2）、updateStat 只 +1（不是 2）。
func TestSettle_ConcurrentLoser(t *testing.T) {
	now := time.Now()
	rp := 10.0
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:             5,
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
		cs:              100000,
		csOk:            true,
		fxRate:          150,
		fxOk:            true,
		insertFirstOnly: true, // 并发模拟：仅第一次 InsertSettlement won=true
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = Settle(context.Background(), d, 5, noElim)
		}()
	}
	wg.Wait()

	if d.insertSettlement != 2 {
		t.Errorf("insertSettlement=%d, want 2（两 goroutine 都调 InsertSettlement）", d.insertSettlement)
	}
	// 核心断言：只有赢家走步骤 8，输家 return nil → 样本/统计只 +1
	if d.upsertVSample != 1 {
		t.Errorf("upsertVSample=%d, want 1（输家不应双计）", d.upsertVSample)
	}
	if d.updateStat != 1 {
		t.Errorf("updateStat=%d, want 1（输家不应双计）", d.updateStat)
	}
}
