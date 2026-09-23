// d427_mt_resolve_test.go —— D-427 I1：MT UNKNOWN 消解工具修正（持仓在场主证据 + 历史 OpenPrice 副证）。
package reconcile

import (
	"context"
	"testing"

	"arb/internal/store/pgstore"
)

// T-I1a 持仓在场：venue 持仓含 ticket → 按持仓权威字段回填（price=OpenPrice, qty=Volume, fee=|Commission|）。
func TestD427MTResolve_OpenedOrderHit(t *testing.T) {
	fs := newFakeStore()
	p := &pgstore.Position{ID: 9, Status: "OPEN"}
	fs.positions[9] = p
	fq := 0.0
	leg := &pgstore.PositionLeg{
		ID: 91, PositionID: 9, LegNo: 0, Phase: "OPEN", Venue: "3001157:mt5",
		Symbol: "XAUUSDm", Side: "buy", Qty: 0.01,
		OrderID: "t88", Status: "UNKNOWN", FilledQty: &fq,
	}
	fs.legs[9] = []*pgstore.PositionLeg{leg}

	r := New(fs,
		&fakeVenueQuerier{venueKey: "3001157:mt5", orders: []VenueOrder{
			{Ticket: "t88", Symbol: "XAUUSDm", Side: "BUY", Volume: 0.02, OpenPrice: 4316.5, Commission: -2.5},
		}},
	)
	r.SetOrderHistoryQueriers(&fakeHistQ{venue: "3001157:mt5", hit: nil}) // 历史无命中——主证据在场即消解
	r.resolveUnknownLegs(context.Background())

	if leg.Status != "FILLED" {
		t.Fatalf("持仓在场应消解 FILLED，got %s", leg.Status)
	}
	if *leg.PriceActual != 4316.5 || *leg.FilledQty != 0.02 {
		t.Fatalf("应按持仓权威字段回填 price=4316.5 qty=0.02，got price=%v qty=%v", *leg.PriceActual, *leg.FilledQty)
	}
	if *leg.FeeUSD != 2.5 {
		t.Fatalf("fee=|commission|=2.5，got %v", *leg.FeeUSD)
	}
}

// T-I1b 持仓无 + 历史命中：按历史 OpenPrice（非 ClosePrice）回填。
func TestD427MTResolve_HistoryOpenPrice(t *testing.T) {
	fs := newFakeStore()
	p := &pgstore.Position{ID: 10, Status: "OPEN"}
	fs.positions[10] = p
	fq := 0.0
	leg := &pgstore.PositionLeg{
		ID: 92, PositionID: 10, LegNo: 0, Phase: "OPEN", Venue: "3001157:mt5",
		Symbol: "XAUUSDm", Side: "buy", Qty: 0.01,
		OrderID: "t89", Status: "UNKNOWN", FilledQty: &fq,
	}
	fs.legs[10] = []*pgstore.PositionLeg{leg}

	r := New(fs, &fakeVenueQuerier{venueKey: "3001157:mt5", orders: nil})
	r.SetOrderHistoryQueriers(&fakeHistQ{venue: "3001157:mt5", hit: &HistoryOrder{
		Ticket: "t89", OpenPrice: 4300.0, ClosePrice: 4320.0, Commission: -1.0,
	}})
	r.resolveUnknownLegs(context.Background())

	if leg.Status != "FILLED" {
		t.Fatalf("历史命中应消解 FILLED，got %s", leg.Status)
	}
	if *leg.PriceActual != 4300.0 {
		t.Fatalf("开仓票应按 OpenPrice=4300 回填（非 ClosePrice=4320），got %v", *leg.PriceActual)
	}
}

// T-I1c 双 miss：持仓无 + 历史无 → stuck 不消解（fail-closed 不变量）。
func TestD427MTResolve_DoubleMissStuck(t *testing.T) {
	fs := newFakeStore()
	p := &pgstore.Position{ID: 11, Status: "OPEN"}
	fs.positions[11] = p
	fq := 0.0
	leg := &pgstore.PositionLeg{
		ID: 93, PositionID: 11, LegNo: 0, Phase: "OPEN", Venue: "3001157:mt5",
		Symbol: "XAUUSDm", Side: "buy", Qty: 0.01,
		OrderID: "t90", Status: "UNKNOWN", FilledQty: &fq,
	}
	fs.legs[11] = []*pgstore.PositionLeg{leg}

	r := New(fs, &fakeVenueQuerier{venueKey: "3001157:mt5", orders: nil})
	r.SetOrderHistoryQueriers(&fakeHistQ{venue: "3001157:mt5", hit: nil})
	r.resolveUnknownLegs(context.Background())

	if leg.Status != "UNKNOWN" {
		t.Fatalf("双 miss 应留 UNKNOWN（stuck），got %s", leg.Status)
	}
}
