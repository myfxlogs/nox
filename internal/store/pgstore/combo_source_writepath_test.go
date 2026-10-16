// combo_source_writepath_test.go —— 双写径血缘锚（D-584 复审④）：approve 径
// graduation+毕业行 id、s2bridge 径 s2_trial+假设行 id——两径断言分位。
package pgstore

import (
	"context"
	"testing"

	"nox/internal/platform/testutil"
)

func TestComboSource_WritePaths(t *testing.T) {
	st, err := New(context.Background(), testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool := st.pool
	ctx := context.Background()
	defer pool.Exec(ctx, `DELETE FROM xasset_allow_combos WHERE canonical_buy LIKE 'WP%'`)

	// ①graduation 径：source+provenance 双列落库
	gid5 := int64(17)
	id, err := st.InsertXAssetAllowCombo(ctx, &XAssetComboInsert{
		CanonicalBuy: "WPA", CanonicalSell: "WPB", VenueBuy: "va:perp", VenueSell: "vb:mt5",
		MaxLongQty: 1, MaxShortQty: 1, MaxGrossUSD: 100,
		Source: "graduation", ProvenanceID: &gid5,
	})
	if err != nil {
		t.Fatalf("graduation 径: %v", err)
	}
	var src string
	var prov *int64
	if err := pool.QueryRow(ctx, `SELECT source, provenance_id FROM xasset_allow_combos WHERE id=$1`, id).Scan(&src, &prov); err != nil {
		t.Fatal(err)
	}
	if src != "graduation" || prov == nil || *prov != 17 {
		t.Fatalf("graduation 血缘: %q %v", src, prov)
	}
	// ②s2_trial 径：source+假设行 id
	hid := int64(505)
	id2, err := st.InsertXAssetAllowCombo(ctx, &XAssetComboInsert{
		CanonicalBuy: "WPC", CanonicalSell: "WPD", VenueBuy: "va:perp", VenueSell: "vb:mt5",
		MaxLongQty: 1, MaxShortQty: 1, MaxGrossUSD: 100,
		Source: "s2_trial", ProvenanceID: &hid,
	})
	if err != nil {
		t.Fatalf("s2_trial 径: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT source, provenance_id FROM xasset_allow_combos WHERE id=$1`, id2).Scan(&src, &prov); err != nil {
		t.Fatal(err)
	}
	if src != "s2_trial" || prov == nil || *prov != 505 {
		t.Fatalf("s2_trial 血缘: %q %v", src, prov)
	}
	// ③source 枚举 CHECK：非法值拒
	if _, err := st.InsertXAssetAllowCombo(ctx, &XAssetComboInsert{
		CanonicalBuy: "WPE", CanonicalSell: "WPF", VenueBuy: "va:perp", VenueSell: "vb:mt5",
		MaxLongQty: 1, MaxShortQty: 1, MaxGrossUSD: 100,
		Source: "bogus",
	}); err == nil {
		t.Fatal("bogus source 应撞 CHECK 拒")
	}
}
