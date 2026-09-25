// d475a_predict_test.go —— D-475a S7/A6 预测-实测对照验收锚：
// 基线=SumCashflowsNet（非 realized_pnl）；偏离>50%→warn flag+alert；≤50% 无副作用；
// 无 paper 行跳过；同键 active 旗去重；realized 差大但 cashflows 一致不插旗。
package settlement

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

func d475aDeps(cashflows float64, accrued float64) *fakeDeps {
	now := time.Now()
	opened := now.Add(-48 * time.Hour) // holdDays>0 否则 ExpectedUsd=0 短路
	rp := 99.0                         // realized_pnl 大偏离——S7 基线非此值，不得插旗
	return &fakeDeps{
		position: &pgstore.Position{
			ID:             9,
			Type:           "XASSET_CARRY",
			Status:         "CLOSED",
			DedupKey:       "XASSET_CARRY:XAUUSD:3001157:mt5:okx:perp",
			RealizedPnl:    &rp,
			OpenedAt:       &opened,
			ClosedAt:       &now,
			ExpectedNetBps: 10,
		},
		legs:         []pgstore.PositionLeg{{Phase: "OPEN", Status: "FILLED", Venue: "3001157:mt5", Qty: 1, PriceActual: ptrFloat(1)}},
		cs:           100,
		csOk:         true,
		fxRate:       1,
		fxOk:         true,
		insertWon:    true,
		cashflowsNet: cashflows,
		papers: []*pgstore.PaperSpotCarry{{
			ID: 7, Venue: "3001157×okx", Symbol: "XAUUSD", AccruedUSD: accrued, Status: "exited",
		}},
	}
}

// 偏离>50% → warn flag+alert。
func TestD475a_S7_DeviationOver50_WarnFlag(t *testing.T) {
	d := d475aDeps(200.0, 100.0) // |200-100|/100=100%>50%
	if err := Settle(context.Background(), d, 9, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if len(d.insertedFlags) != 1 || d.insertedFlags[0].Level != "warn" ||
		d.insertedFlags[0].Venue != "3001157×okx" || d.insertedFlags[0].Symbol != "XAUUSD" {
		t.Fatalf("flags=%+v", d.insertedFlags)
	}
	if len(d.insertAlertMsgs) == 0 {
		t.Fatal("偏离>50% 应落 warn alert")
	}
}

// ≤50% → 无副作用；realized_pnl 大偏离但 cashflows 相符同样不插旗。
func TestD475a_S7_Within50_NoSideEffect(t *testing.T) {
	d := d475aDeps(140.0, 100.0) // 40%≤50%；realized=99 vs accrued=100 差 1% 也无关
	if err := Settle(context.Background(), d, 9, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if len(d.insertedFlags) != 0 {
		t.Fatalf("≤50%% 不得插旗，got %+v", d.insertedFlags)
	}
}

// 无 paper 行→跳过；同键 active 旗已存在→去重不刷。
func TestD475a_S7_NoPaper_And_Dedup(t *testing.T) {
	d := d475aDeps(200.0, 100.0)
	d.papers = nil
	if err := Settle(context.Background(), d, 9, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if len(d.insertedFlags) != 0 {
		t.Fatal("无 paper 行应跳过对照")
	}
	d2 := d475aDeps(200.0, 100.0)
	d2.activeFlags = map[string]pgstore.VenueSymbolFlag{
		pgstore.FlagKey("3001157×okx", "XAUUSD"): {Venue: "3001157×okx", Symbol: "XAUUSD", Level: "warn"},
	}
	if err := Settle(context.Background(), d2, 9, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if len(d2.insertedFlags) != 0 {
		t.Fatal("同键 active 旗应去重跳过")
	}
}

// 非 XASSET_CARRY 不触发。
func TestD475a_S7_NonXasset_Skip(t *testing.T) {
	d := d475aDeps(200.0, 100.0)
	d.position.Type = "CROSS_VENUE"
	if err := Settle(context.Background(), d, 9, noElim, nil); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	if len(d.insertedFlags) != 0 {
		t.Fatal("非 XASSET_CARRY 不应对照")
	}
}
