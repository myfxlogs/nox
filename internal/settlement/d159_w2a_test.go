// d159_w2a_test.go —— D-159 W2a S7：settlement 失败必须返回 error（可重试状态）。
// A9：UpsertVerificationSample 失败 → Settle 返回 error；UpdateStrategyStat 失败 → Settle 返回 error。
package settlement

import (
	"context"
	"errors"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// failingDeps 包装 fakeDeps，让指定方法返回 error。
type failingDeps struct {
	*fakeDeps
	upsertErr error
	updateErr error
}

func (f *failingDeps) UpsertVerificationSample(ctx context.Context, vkey string, expectedUsd, realizedUsd float64, closedAt time.Time) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	return f.fakeDeps.UpsertVerificationSample(ctx, vkey, expectedUsd, realizedUsd, closedAt)
}

func (f *failingDeps) UpdateStrategyStat(ctx context.Context, strategy string, realizedPnl float64) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	return f.fakeDeps.UpdateStrategyStat(ctx, strategy, realizedPnl)
}

// TestSettle_UpsertFails_ReturnsError A9：UpsertVerificationSample 失败 → Settle 返回 error（不只打日志）。
func TestSettle_UpsertFails_ReturnsError(t *testing.T) {
	now := time.Now()
	rp := 10.0
	base := &fakeDeps{
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
		cs:           100000,
		csOk:         true,
		fxRate:       1.0,
		fxOk:         true,
		insertWon:    true,
		cashflowsNet: 0,
	}
	d := &failingDeps{fakeDeps: base, upsertErr: errors.New("db down")}

	err := Settle(context.Background(), d, 1, noElim)
	if err == nil {
		t.Fatal("Settle 应返回 error（upsert 失败），但返回 nil")
	}
}

// TestSettle_UpdateStatFails_ReturnsError A9：UpdateStrategyStat 失败 → Settle 返回 error。
func TestSettle_UpdateStatFails_ReturnsError(t *testing.T) {
	now := time.Now()
	rp := 10.0
	base := &fakeDeps{
		position: &pgstore.Position{
			ID:             2,
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
		cs:           100000,
		csOk:         true,
		fxRate:       1.0,
		fxOk:         true,
		insertWon:    true,
		cashflowsNet: 0,
	}
	d := &failingDeps{fakeDeps: base, updateErr: errors.New("db down")}

	err := Settle(context.Background(), d, 2, noElim)
	if err == nil {
		t.Fatal("Settle 应返回 error（updateStat 失败），但返回 nil")
	}
}

// TestSettle_HappyPath_StillSucceeds A9 回归：正常路径仍返回 nil（不破坏 happy path）。
func TestSettle_HappyPath_StillSucceeds(t *testing.T) {
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
		cs:           100000,
		csOk:         true,
		fxRate:       1.0,
		fxOk:         true,
		insertWon:    true,
		cashflowsNet: 0,
	}

	err := Settle(context.Background(), d, 3, noElim)
	if err != nil {
		t.Fatalf("Settle happy path 应返回 nil，但返回: %v", err)
	}
}
