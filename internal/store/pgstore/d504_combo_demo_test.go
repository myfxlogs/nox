// d504_combo_demo_test.go D-504 S1 真库锚：ListXAssetAllowCombosAll 滤 simulated=t
// 行（物理行留库不删），NULL/false 视同非 demo 保留。需 ARB_TEST_PG_DSN。
package pgstore

import (
	"context"
	"testing"

	"arb/internal/testutil"
)

func TestD504_ComboListFiltersDemo(t *testing.T) {
	st, err := New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool := st.pool
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM xasset_allow_combos WHERE canonical_buy='D504X'`)
	})
	tr, fl := true, false
	idDemo, err := st.InsertXAssetAllowCombo(ctx, &XAssetComboInsert{
		CanonicalBuy: "D504X", CanonicalSell: "D504Y", VenueBuy: "a:perp", VenueSell: "b:perp",
		MaxLongQty: 1, MaxShortQty: 1, MaxGrossUSD: 100, Simulated: tr})
	if err != nil {
		t.Fatalf("insert demo: %v", err)
	}
	idReal, err := st.InsertXAssetAllowCombo(ctx, &XAssetComboInsert{
		CanonicalBuy: "D504X", CanonicalSell: "D504Z", VenueBuy: "c:perp", VenueSell: "d:perp",
		MaxLongQty: 1, MaxShortQty: 1, MaxGrossUSD: 100, Simulated: fl})
	if err != nil {
		t.Fatalf("insert real: %v", err)
	}
	rows, err := st.ListXAssetAllowCombosAll(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var demoSeen, realSeen bool
	for _, r := range rows {
		if r.ID == idDemo {
			demoSeen = true
		}
		if r.ID == idReal {
			realSeen = true
		}
	}
	if demoSeen {
		t.Error("simulated=t 行应从管理面滤除（物理行留库）")
	}
	if !realSeen {
		t.Error("simulated=f 行应保留")
	}
	// 物理行仍在库（残留只收展示不删行）
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM xasset_allow_combos WHERE id=$1`, idDemo).Scan(&n); err != nil || n != 1 {
		t.Fatalf("demo 物理行应留库: n=%d err=%v", n, err)
	}
}
