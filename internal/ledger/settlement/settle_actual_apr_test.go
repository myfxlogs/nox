// settle_actual_apr_test.go —— D-527② 锚：提案回链仓位终态 actual_apr 回写。
package settlement

import (
	"context"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// aprDeps 在 fakeDeps 上挂 ActualAPRWriter 面（同 hearDeps 包装式）。
type aprDeps struct {
	*fakeDeps
	linkPID *int64            // ProposalForClosedPosition 返回值
	actuals map[int64]float64 // UpdateOppEvalActual 捕获
}

func (d *aprDeps) ProposalForClosedPosition(context.Context, int64) (*int64, error) {
	return d.linkPID, nil
}
func (d *aprDeps) UpdateOppEvalActual(_ context.Context, pid int64, apr float64) error {
	d.actuals[pid] = apr
	return nil
}

func aprFixture(now time.Time) *fakeDeps {
	rp := 10.0
	open := now.Add(-4 * time.Hour) // <12h：fundingDue 闸不触发（funding 实收查径免干扰）
	return &fakeDeps{
		position: &pgstore.Position{
			ID: 1, OpportunityID: "CARRY:AUDJPY:x:y", Type: "CARRY",
			Status: "CLOSED", ExpectedNetBps: 15.0, RealizedPnl: &rp,
			OpenedAt: &open, ClosedAt: &now,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "52993526:mt5", Qty: 1.0, PriceActual: ptrFloat(113.5)},
		},
		cs: 100000, csOk: true, fxRate: 150, fxOk: true, insertWon: true,
		cashflowsQuality: "actual", // CARRY 型有 cashflow 面——fake 注 actual 走 applied 径
	}
}

func TestSettle_ActualAPRWriteback(t *testing.T) {
	now := time.Now()
	pid := int64(77)
	d := &aprDeps{fakeDeps: aprFixture(now), linkPID: &pid, actuals: map[int64]float64{}}
	if err := Settle(context.Background(), d, 1, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	// 回写值=净额/名义×365/持有天数×100（读 settlements 行同口径）。
	row := d.lastSettlementRow
	if row == nil || row.NetRealizedUsd == nil || row.NotionalUsd == nil {
		t.Fatalf("settlement row missing: %+v", row)
	}
	want := *row.NetRealizedUsd / *row.NotionalUsd * 365 / row.HoldDays * 100
	got, ok := d.actuals[pid]
	if !ok {
		t.Fatalf("actual_apr 未回写: %+v", d.actuals)
	}
	if got < want*0.999 || got > want*1.001 {
		t.Fatalf("actual_apr=%v want≈%v", got, want)
	}
}

func TestSettle_ActualAPRNoLinkNoWrite(t *testing.T) {
	now := time.Now()
	// 无提案回链（引擎直产仓）→ 不写≠0（无对应执行事件不编数）。
	d := &aprDeps{fakeDeps: aprFixture(now), linkPID: nil, actuals: map[int64]float64{}}
	if err := Settle(context.Background(), d, 1, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if len(d.actuals) != 0 {
		t.Fatalf("无回链误写: %+v", d.actuals)
	}
}
