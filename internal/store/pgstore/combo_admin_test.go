// d471_combo_admin_test.go D-471 S3 真库锚：upsert 行锁更新 + 在持仓查询 + 管理面全量读。
package pgstore

import (
	"context"
	"testing"

	"nox/internal/platform/testutil"
)

func TestD471_ComboAdmin(t *testing.T) {
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
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM xasset_allow_combos WHERE canonical_buy='D471X'`)
	}()
	// insert（走 approve 同路径）→ ListAll 立见
	id, err := st.InsertXAssetAllowCombo(ctx, &XAssetComboInsert{
		CanonicalBuy: "D471X", CanonicalSell: "D471Y", VenueBuy: "a:perp", VenueSell: "b:perp",
		MaxLongQty: 1, MaxShortQty: 1, MaxGrossUSD: 1000})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListXAssetAllowCombosAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *XAssetComboAdminRow
	for i := range rows {
		if rows[i].ID == id {
			found = &rows[i]
		}
	}
	if found == nil || !found.Enabled {
		t.Fatalf("inserted row 未在 ListAll 立见: %+v", rows)
	}
	// 行锁 update：caps+enabled 落库+读回
	ok, err := st.UpdateXAssetAllowCombo(ctx, &XAssetComboAdminRow{
		ID: id, MaxLongQty: 2, MaxShortQty: 3, MaxGrossUSD: 900, Enabled: false, Note: "t"})
	if err != nil || !ok {
		t.Fatalf("update ok=%v err=%v", ok, err)
	}
	rows, _ = st.ListXAssetAllowCombosAll(ctx)
	for _, r := range rows {
		if r.ID == id {
			if r.Enabled || r.MaxLongQty != 2 || r.MaxShortQty != 3 || r.MaxGrossUSD != 900 {
				t.Errorf("update 未落库: %+v", r)
			}
		}
	}
	// enabled=false 行不入执行视图（LoadXAssetAllowCombos 过滤实证）
	execRows, err := st.LoadXAssetAllowCombos(ctx, "real")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range execRows {
		if r.CanonicalBuy == "D471X" {
			t.Error("禁用行不得入执行视图")
		}
	}
	// 不存在行 → ok=false 不误报
	ok, err = st.UpdateXAssetAllowCombo(ctx, &XAssetComboAdminRow{ID: 999999, MaxLongQty: 1, MaxShortQty: 1, MaxGrossUSD: 1, Enabled: true})
	if ok || err != nil {
		t.Errorf("missing row update ok=%v err=%v", ok, err)
	}
	// 在持仓查询：构造 OPEN 仓+双腿命中四元组
	var posID int64
	err = pool.QueryRow(ctx, `INSERT INTO positions (opportunity_id, type, status, dedup_key, notional_usd, expected_net_bps, opened_at)
		VALUES ('D471:OPP:X:a:b','XASSET_CARRY','OPEN','XASSET:D471X:a:perp:b:perp',200,0,now()) RETURNING id`).Scan(&posID)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM positions WHERE id=$1`, posID)
	for i, l := range []struct{ side, venue string }{{"BUY", "a:perp"}, {"SELL", "b:perp"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status)
			VALUES ($1,$2,'OPEN',split_part($3,':',1),split_part($3,':',2),'SYM',$4,1,1,'FILLED')`, posID, i+1, l.venue, l.side); err != nil {
			t.Fatal(err)
		}
	}
	ids, notional, err := st.ListOpenPositionsForCombo(ctx, "D471X", "D471Y", "a:perp", "b:perp")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != posID || notional != 200 {
		t.Fatalf("open pos match ids=%v notional=%v", ids, notional)
	}
	// 反向 venue（腿序颠倒）不命中
	ids, _, _ = st.ListOpenPositionsForCombo(ctx, "D471X", "D471Y", "b:perp", "a:perp")
	if len(ids) != 0 {
		t.Error("腿序颠倒不得命中")
	}
}

// TestNullCap_PctRow NULL 帽双修锚（D-582）：
// ①pct 档腿帽 NULL 合法——ListAll 裸扫 float64 不崩（COALESCE→0）；
// ②Update pct 行（toggle/note）帽列仍 NULL 不撞 CHECK（防 COALESCE 读值回写）；
// ③fixed 行正常读写帽。
func TestNullCap_PctRow(t *testing.T) {
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
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM xasset_allow_combos WHERE canonical_buy LIKE 'NCAP%'`)
	}()

	// pct 行直种（sizing_mode=pct+funding_pct>0，帽列 NULL=1038 CHECK 合法态）
	var pid int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell,
			sizing_mode, funding_pct, enabled, note)
		VALUES ('NCAPBUY','NCAPSELL','a','perp','b','perp','pct',0.1,false,'pct行') RETURNING id`).Scan(&pid); err != nil {
		t.Fatalf("seed pct row: %v", err)
	}
	// fixed 行对照
	fid, err := st.InsertXAssetAllowCombo(ctx, &XAssetComboInsert{
		CanonicalBuy: "NCAPFB", CanonicalSell: "NCAPFS", VenueBuy: "a:perp", VenueSell: "b:perp",
		MaxLongQty: 1, MaxShortQty: 1, MaxGrossUSD: 100})
	if err != nil {
		t.Fatal(err)
	}

	// ① ListAll 裸扫不崩（COALESCE→0）
	rows, err := st.ListXAssetAllowCombosAll(ctx)
	if err != nil {
		t.Fatalf("ListAll 崩: %v", err)
	}
	var prow *XAssetComboAdminRow
	for i := range rows {
		if rows[i].ID == pid {
			prow = &rows[i]
		}
	}
	if prow == nil {
		t.Fatal("pct 行未读出")
	}
	// unset 语义=0 值行（消费端走未设上限分支——读径已 COALESCE 先例）

	// ② Update pct 行（把读出的 0 值当入参回写 toggle+note）——帽列必须仍 NULL
	prow.Enabled = true
	prow.Note = "pct行改"
	ok, err := st.UpdateXAssetAllowCombo(ctx, prow)
	if err != nil || !ok {
		t.Fatalf("Update pct 行: ok=%v err=%v", ok, err)
	}
	var lq, sq, gr, nt *float64
	if err := pool.QueryRow(ctx, `
		SELECT max_long_qty, max_short_qty, max_gross_usd, notional_usd
		FROM xasset_allow_combos WHERE id=$1`, pid).Scan(&lq, &sq, &gr, &nt); err != nil {
		t.Fatal(err)
	}
	if lq != nil || sq != nil || gr != nil || nt != nil {
		t.Fatalf("pct 行帽列被污染写回: %v %v %v %v", lq, sq, gr, nt)
	}

	// ③ fixed 行帽照常读写
	ok, err = st.UpdateXAssetAllowCombo(ctx, &XAssetComboAdminRow{
		ID: fid, MaxLongQty: 2, MaxShortQty: 2, MaxGrossUSD: 200, Enabled: true, Note: "fx"})
	if err != nil || !ok {
		t.Fatalf("Update fixed 行: ok=%v err=%v", ok, err)
	}
	var flq float64
	if err := pool.QueryRow(ctx, `SELECT max_long_qty FROM xasset_allow_combos WHERE id=$1`, fid).Scan(&flq); err != nil {
		t.Fatal(err)
	}
	if flq != 2 {
		t.Fatalf("fixed 行帽写败: %v", flq)
	}
}
