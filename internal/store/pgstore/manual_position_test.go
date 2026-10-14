package pgstore

import (
	"context"
	"testing"

	"nox/internal/platform/testutil"
)

// TestD512_ManualPosition：手工仓位台账 CRUD——插/改/删+校验闸（qty<=0 拒写）。
func TestD512_ManualPosition(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	px := 1.23
	r := &ManualPosition{VenueLabel: "cmc", Symbol: "BTC", Qty: 0.5, PriceUSD: &px, Note: "手工底仓", Enabled: true}
	if err := st.UpsertManualPosition(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r.ID == 0 {
		t.Fatal("插行未回 id")
	}
	rows, err := st.ListManualPositions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got *ManualPosition
	for i := range rows {
		if rows[i].ID == r.ID {
			got = &rows[i]
		}
	}
	if got == nil || got.Symbol != "BTC" || got.Qty != 0.5 || got.PriceUSD == nil || *got.PriceUSD != 1.23 {
		t.Fatalf("插行对拍败：%+v", got)
	}
	// 更新径：价撤空+禁用标。
	got.PriceUSD = nil
	got.Enabled = false
	if err := st.UpsertManualPosition(ctx, got); err != nil {
		t.Fatal(err)
	}
	rows2, _ := st.ListManualPositions(ctx)
	for i := range rows2 {
		if rows2[i].ID == r.ID && (rows2[i].PriceUSD != nil || rows2[i].Enabled) {
			t.Fatalf("更新未落：%+v", rows2[i])
		}
	}
	// 校验闸：qty<=0 / symbol 空拒写。
	if err := st.UpsertManualPosition(ctx, &ManualPosition{Symbol: "X", Qty: 0}); err == nil {
		t.Fatal("qty=0 应拒写")
	}
	if err := st.UpsertManualPosition(ctx, &ManualPosition{Symbol: "", Qty: 1}); err == nil {
		t.Fatal("symbol 空应拒写")
	}
	if err := st.DeleteManualPosition(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteManualPosition(ctx, r.ID); err == nil {
		t.Fatal("删不存在行应错")
	}
}
