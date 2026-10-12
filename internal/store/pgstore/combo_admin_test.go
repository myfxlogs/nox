// d471_combo_admin_test.go D-471 S3 真库锚：upsert 行锁更新 + 在持仓查询 + 管理面全量读。
package pgstore

import (
	"context"
	"testing"

	"arb/internal/platform/testutil"
)

func TestD471_ComboAdmin(t *testing.T) {
	st, err := New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
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
