// spot_snapshot_rows_test.go —— D-528：spot 分币快照行 + DCI 台账对拍锚。
package dashboard

import (
	"context"
	"testing"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/ledger/reconcile"
	"nox/internal/store/pgstore"
)

// TestSnapshotRowsSpotMultiCcy：binance:spot 分币行——USDT/BTC 各币种行，
// free_margin=FreeQtyByCcy 原生量；prev 消失币补零。
func TestSnapshotRowsSpotMultiCcy(t *testing.T) {
	prev := &reconcile.AccountInfo{Holdings: []*v1.Holding{{Ccy: "TRX", Qty: 5}}}
	acct := &reconcile.AccountInfo{
		Venue: "binance", VenueType: "spot", AssetType: "cex", Currency: "USDT",
		Balance: 1100, Equity: 1100, FreeMargin: 500,
		Holdings: []*v1.Holding{
			{Ccy: "USDT", Qty: 500, UsdtValue: 500},
			{Ccy: "BTC", Qty: 0.01, UsdtValue: 600},
		},
		FreeQtyByCcy: map[string]float64{"USDT": 400, "BTC": 0.01},
	}
	rows := snapshotRows(prev, acct)
	per := map[string]pgstore.VenueAccountSnapshot{}
	for _, r := range rows {
		if r.VenueType != "spot" || r.Venue != "binance" {
			t.Fatalf("row venue keys wrong: %+v", r)
		}
		per[r.Currency] = r
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 行（USDT+BTC+TRX 补零）, got %+v", rows)
	}
	if per["USDT"].EquityRaw != 500 || per["USDT"].FreeMarginRaw != 400 {
		t.Errorf("USDT 行错: %+v", per["USDT"])
	}
	if per["BTC"].EquityRaw != 0.01 || per["BTC"].FreeMarginRaw != 0.01 {
		t.Errorf("BTC 行错（原生量零换算）: %+v", per["BTC"])
	}
	if per["TRX"].EquityRaw != 0 {
		t.Errorf("消失币 TRX 补零行缺: %+v", per["TRX"])
	}
}

// TestCheckDCILedgerCoverage：台账 filled 单 product_ref 缺席 venue 在仓集 → warn 告警。
func TestCheckDCILedgerCoverage(t *testing.T) {
	fs := &fakeYieldStore{dciOrders: []pgstore.FilledDCIOrder{
		{ID: 7, Venue: "binance", ProductRef: "DCI-P1"},
		{ID: 8, Venue: "binance", ProductRef: "DCI-MISS"},
		{ID: 9, Venue: "okx", ProductRef: "OTHER-VENUE"},
	}}
	m := &AccountMonitor{yieldStore: fs}
	m.checkDCILedgerCoverage(context.Background(), &reconcile.AccountInfo{
		Venue: "binance", VenueType: "earn", DCIOpenRefs: []string{"99", "DCI-P1"},
	})
	if len(fs.alerts) != 1 {
		t.Fatalf("want 1 告警（DCI-MISS 断点）, got %v", fs.alerts)
	}
}
