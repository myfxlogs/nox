package settlement

import (
	"context"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// T7：settle 接线 bool 推导——perp FILLED 腿→hasFundingLeg；FAILED→false；
// holdDays=0.3→fundingDue=false、1.5→true。
func TestD438_T7_LegDerivation(t *testing.T) {
	base := func(openHours float64, legs []pgstore.PositionLeg) *fakeDeps {
		opened := time.Now().Add(-time.Duration(openHours * float64(time.Hour)))
		closed := time.Now()
		rp := 1.0
		return &fakeDeps{
			position: &pgstore.Position{
				ID:             1,
				OpportunityID:  "XASSET_CARRY:XAU:a:mt5:okx:perp",
				Type:           "XASSET_CARRY",
				Status:         "CLOSED",
				ExpectedNetBps: 15.0,
				RealizedPnl:    &rp,
				OpenedAt:       &opened,
				ClosedAt:       &closed,
			},
			legs:      legs,
			cs:        1,
			csOk:      true,
			fxRate:    1,
			fxOk:      true,
			insertWon: true,
		}
	}
	mtLeg := pgstore.PositionLeg{Phase: "OPEN", Status: "FILLED", Venue: "3001157:mt5", Qty: 1, PriceActual: ptrFloat(1)}
	perpFilled := pgstore.PositionLeg{Phase: "OPEN", Status: "FILLED", Venue: "okx:perp", Qty: 1, PriceActual: ptrFloat(1)}
	perpFailed := pgstore.PositionLeg{Phase: "OPEN", Status: "FAILED", Venue: "okx:perp", Qty: 1, PriceActual: ptrFloat(1)}

	// perp FILLED + 36h → true/true
	d := base(36, []pgstore.PositionLeg{mtLeg, perpFilled})
	if err := Settle(context.Background(), d, 1, noElim, nil); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if !d.gotHasFundingLeg || !d.gotFundingDue {
		t.Fatalf("perp FILLED 36h: gotFundingLeg=%v gotDue=%v want true/true", d.gotHasFundingLeg, d.gotFundingDue)
	}
	if d.forSettleCalls != 1 {
		t.Fatalf("forSettleCalls=%d want 1", d.forSettleCalls)
	}
	// perp FAILED → false
	d = base(36, []pgstore.PositionLeg{mtLeg, perpFailed})
	if err := Settle(context.Background(), d, 1, noElim, nil); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if d.gotHasFundingLeg {
		t.Fatalf("perp FAILED: gotFundingLeg=true want false")
	}
	// perp FILLED + 7.2h(0.3d) → true/false
	d = base(7.2, []pgstore.PositionLeg{mtLeg, perpFilled})
	if err := Settle(context.Background(), d, 1, noElim, nil); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if !d.gotHasFundingLeg || d.gotFundingDue {
		t.Fatalf("0.3d: gotFundingLeg=%v gotDue=%v want true/false", d.gotHasFundingLeg, d.gotFundingDue)
	}
}

// T8：端到端——perp 腿仓 cashflows 组件（income $1.03 + commission -$2.18）参与 quality 判定；
// ②：net=realized_pnl 单源净额（exec 已含现金流），actual 口径入统计。
func TestD438_T8_EndToEnd(t *testing.T) {
	opened := time.Now().Add(-48 * time.Hour)
	closed := time.Now()
	rp := 5.0
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:             1,
			OpportunityID:  "XASSET_CARRY:XAU:a:mt5:okx:perp",
			Type:           "XASSET_CARRY",
			Status:         "CLOSED",
			ExpectedNetBps: 15.0,
			RealizedPnl:    &rp,
			OpenedAt:       &opened,
			ClosedAt:       &closed,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "3001157:mt5", Qty: 1, PriceActual: ptrFloat(1)},
			{Phase: "OPEN", Status: "FILLED", Venue: "okx:perp", Qty: 1, PriceActual: ptrFloat(1)},
		},
		cs:               1,
		csOk:             true,
		fxRate:           1,
		fxOk:             true,
		cashflowsNet:     -1.15, // income 1.03 + commission -2.18（fake 预设=ForSettle 返回）
		cashflowsQuality: "actual",
		insertWon:        true,
	}
	if err := Settle(context.Background(), d, 1, noElim, nil); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if d.forSettleCalls != 1 {
		t.Fatalf("SumCashflowsNetForSettle 未被调用（走了旧轨）")
	}
	if !approx(d.lastNetPnl, 5.0, 1e-6) {
		t.Fatalf("netPnl=%v want 5.0（realized_pnl=净额单源）", d.lastNetPnl)
	}
	if d.lastSettlementRow == nil || d.lastSettlementRow.PnlQuality != "actual" {
		t.Fatalf("pnlQuality=%v want actual", d.lastSettlementRow)
	}
}
