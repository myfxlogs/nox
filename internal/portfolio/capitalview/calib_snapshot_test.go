package capitalview

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/ledger/reconcile"
	"nox/internal/store/pgstore"
)

type fakeQ struct{ acct *reconcile.AccountInfo }

func (f *fakeQ) Account(context.Context) (*reconcile.AccountInfo, error) { return f.acct, nil }
func (f *fakeQ) VenueKey() string                                        { return f.acct.VenueKey }

// D-523①：REST 校准成功且值变 → rest_calib 快照行；同值零写；sim 不落库。
func TestD523_CalibSnapshotWrites(t *testing.T) {
	ctx := context.Background()
	var rows []pgstore.VenueAccountSnapshot
	fq := &fakeQ{acct: &reconcile.AccountInfo{
		VenueKey: "binance:perp", Venue: "binance", VenueType: "perp", AssetType: "cex",
		Balance: 1344, Equity: 1344, FreeMargin: 1344, Currency: "USDT",
	}}
	v := New(Deps{
		Ledger: bus.NewBalanceLedger(),
		QuerierFor: func(context.Context, string) (reconcile.AccountQuerier, string, bool) {
			return fq, "binance:perp", true
		},
		SnapWrite: func(_ context.Context, snaps []pgstore.VenueAccountSnapshot) error {
			rows = append(rows, snaps...)
			return nil
		},
		RestTTL: func() time.Duration { return 0 }, // 每读强制校准
	})
	if _, ok := v.Live(ctx, "binance:perp"); !ok {
		t.Fatal("live miss")
	}
	if len(rows) != 1 || rows[0].Src != "rest_calib" || rows[0].EquityRaw != 1344 ||
		rows[0].Venue != "binance" || rows[0].VenueType != "perp" || rows[0].FreeMarginRaw != 1344 {
		t.Fatalf("rows=%+v", rows)
	}
	v.LiveFresh(ctx, "binance:perp") // force 重校同值 → 零写
	if len(rows) != 1 {
		t.Fatalf("同值重复写: %d", len(rows))
	}
	fq.acct = &reconcile.AccountInfo{
		VenueKey: "binance:perp", Venue: "binance", VenueType: "perp", AssetType: "cex",
		Balance: 1300, Equity: 1300, FreeMargin: 1300, Currency: "USDT",
	}
	v.LiveFresh(ctx, "binance:perp")
	if len(rows) != 2 {
		t.Fatalf("值变未写: %d", len(rows))
	}
}
