// combo_source_test.go —— D-584 出处约束锚：NULL 出处禁 enabled/graduation
// 出处过/venue 双簿消失自动退役。
package pgstore

import (
	"context"
	"strings"
	"testing"

	"nox/internal/platform/testutil"
)

func TestComboSourceGate(t *testing.T) {
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
	defer pool.Exec(ctx, `DELETE FROM xasset_allow_combos WHERE canonical_buy LIKE 'SRCG%'`)

	// ①NULL 出处+enabled → CHECK 拒（写径 NULL 不补即拦）
	if _, err := pool.Exec(ctx, `INSERT INTO xasset_allow_combos
		(canonical_buy,canonical_sell,venue_buy,venue_type_buy,venue_sell,venue_type_sell,
		 max_long_qty,max_short_qty,max_gross_usd,enabled,source)
		VALUES ('SRCGA','SRCGB','vta','perp','vtb','perp',1,1,100,true,NULL)`); err == nil {
		t.Fatal("NULL 出处 enabled 应撞 CHECK 拒")
	}
	// ②graduation 出处+provenance → 过
	gid, err := st.InsertXAssetAllowCombo(ctx, &XAssetComboInsert{
		CanonicalBuy: "SRCGB", CanonicalSell: "SRCGC",
		VenueBuy: "vta:perp", VenueSell: "vtb:perp",
		MaxLongQty: 1, MaxShortQty: 1, MaxGrossUSD: 100,
		Source: "graduation",
	})
	if err != nil {
		t.Fatalf("graduation 出处应过: %v", err)
	}
	var src string
	if err := pool.QueryRow(ctx, `SELECT source FROM xasset_allow_combos WHERE id=$1`, gid).Scan(&src); err != nil || src != "graduation" {
		t.Fatalf("source 落库: %q err=%v", src, err)
	}
	// ③enabled 行 venue 双簿消失 → 退役守卫自动摘牌+留痕
	if n, err := st.RetireCombosMissingVenue(ctx); err != nil || n == 0 {
		t.Fatalf("退役守卫: n=%d err=%v", n, err)
	}
	var en bool
	var note string
	if err := pool.QueryRow(ctx, `SELECT enabled,note FROM xasset_allow_combos WHERE id=$1`, gid).Scan(&en, &note); err != nil {
		t.Fatal(err)
	}
	if en {
		t.Fatal("venue 消失行应自动 enabled=false")
	}
	if !strings.Contains(note, "venue退役") {
		t.Fatalf("退役留痕缺: %q", note)
	}
}
