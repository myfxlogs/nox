package settlement

import (
	"context"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// TestD047_ProbeSettleSkipped D-047④：type='PROBE' 仓写 settlement 审计行
// （stats_status='skipped'）但永不走步骤 9 的样本/策略统计/淘汰面。
func TestD047_ProbeSettleSkipped(t *testing.T) {
	now := time.Now()
	rp := -3.5
	d := &fakeDeps{
		position: &pgstore.Position{
			ID:            1,
			OpportunityID: "PROBE:XAUUSD:D-047-t1",
			Type:          pgstore.PositionTypePROBE,
			Status:        "CLOSED",
			RealizedPnl:   &rp,
			OpenedAt:      &now,
			ClosedAt:      &now,
		},
		legs: []pgstore.PositionLeg{
			{Phase: "OPEN", Status: "FILLED", Venue: "3001:mt5", Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01, PriceActual: ptrFloat(2000)},
		},
		cs:        100,
		csOk:      true,
		fxRate:    1,
		fxOk:      true,
		insertWon: true,
	}
	if err := Settle(context.Background(), d, 1, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if d.insertSettlement != 1 {
		t.Fatalf("insertSettlement=%d, want 1（skipped 审计行仍须落）", d.insertSettlement)
	}
	if d.lastSettlementRow == nil || d.lastSettlementRow.StatsStatus != "skipped" {
		t.Fatalf("stats_status=%v, want skipped", d.lastSettlementRow)
	}
	if d.updateStat != 0 {
		t.Errorf("UpdateStrategyStat 被调 %d 次（PROBE 永不得入策略统计）", d.updateStat)
	}
	if d.disableStrategy != "" {
		t.Errorf("DisableStrategy(%q) 被调（PROBE 永不得触发淘汰）", d.disableStrategy)
	}
}
