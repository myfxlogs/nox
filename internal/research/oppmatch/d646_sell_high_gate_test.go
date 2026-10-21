// d646_sell_high_gate_test.go —— sell_high 双闸锚：无券不产卡/券足放行/
// 结构缺 belt/约束缺 fail-closed/券不足覆盖申购档。
package oppmatch

import (
	"context"
	"testing"

	"nox/internal/risk/gate"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

func d646Store(t *testing.T) (*pgstore.Store, context.Context) {
	t.Helper()
	sigmaFn = func(context.Context, *pgstore.Store) (float64, string, error) {
		return 0.001, "test-stub", nil
	}
	t.Cleanup(func() { sigmaFn = nil })
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DELETE FROM fin_cex_dci_quote WHERE product_ref LIKE 'D646%'`,
		`DELETE FROM fin_capability_constraint WHERE subject LIKE 'd646%' OR subject='custody_share@okx'`,
		`DELETE FROM personal_positions WHERE asset='BTC' AND kind='other'`,
		`DELETE FROM fin_cex_spread WHERE venue_buy LIKE 'd646%'`,
		`DELETE FROM app_config WHERE key='sell_high_min_qty'`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	// spot=90000；σ=0.001、k=1.5、tenor=14 → CALL 界=90000×(1+0.0037)≈90333，
	// fixture strike=95000 罩内。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO fin_cex_spread (symbol, venue_buy, venue_sell, price_buy, price_sell, spread_bps)
		 VALUES ('BTC','d646a','d646b',90000,90000,0)`); err != nil {
		t.Fatal(err)
	}
	return st, ctx
}

// d646Cons 只插自建 custody_share@okx——σ 尾险键（dci_call_otm_k=1.5/
// dci_tenor_max_days=30）用 1027 迁移种子，禁删共享行（前版删种害 D526 红）。
func d646Cons(t *testing.T, st *pgstore.Store, ctx context.Context) {
	t.Helper()
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO fin_capability_constraint (scope, subject, qualifier, value_num) VALUES
		('global','custody_share@okx','',1.0)`); err != nil {
		t.Fatal(err)
	}
}

func d646Quote(t *testing.T, st *pgstore.Store, ctx context.Context, apy float64) {
	t.Helper()
	tenor := 7
	strike := 95000.0
	if err := st.InsertCexDCIQuote(ctx, &pgstore.CexDCIQuote{
		Venue: "okx", Underlying: "BTC", InvestCcy: "BTC", OptType: "CALL",
		Strike: &strike, TenorDays: &tenor, APY: apy, ProductRef: "D646P",
	}); err != nil {
		t.Fatal(err)
	}
}

func d646Pool(t *testing.T, st *pgstore.Store, ctx context.Context, qty float64) {
	t.Helper()
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO personal_positions (asset, kind, price_symbol, qty) VALUES ('BTC','other','BTCUSDT',$1)`, qty); err != nil {
		t.Fatal(err)
	}
}

// 持仓闸：池零行=无券不产（#1687 病灶复现锚）。
func TestD646_NoHoldingBlocks(t *testing.T) {
	st, ctx := d646Store(t)
	d646Cons(t, st, ctx)
	d646Quote(t, st, ctx, 0.30)
	ok, why, _ := sellHighGate(ctx, st, "okx")
	if ok {
		t.Fatalf("无券应拒: %s", why)
	}
	t.Logf("拒因=%s", why)
}

// 券足（0.5×1.0=0.5≥0.01）+σ 界内达标档 → 放行注记带券/σ/界/净额。
func TestD646_HoldingAndStructPass(t *testing.T) {
	st, ctx := d646Store(t)
	d646Cons(t, st, ctx)
	d646Pool(t, st, ctx, 0.5)
	d646Quote(t, st, ctx, 0.30)
	ok, why, sel := sellHighGate(ctx, st, "okx")
	if !ok {
		t.Fatalf("券足+达标档应放行: %s", why)
	}
	// 脱节修锚：闸实选档回传——prem=该档 apy、row=实选行（提案 APR 按此记）。
	if sel == nil || sel.row == nil || sel.row.ProductRef != "D646P" || sel.prem != 0.30 {
		t.Fatalf("实选档回传不符: %+v", sel)
	}
	t.Logf("注记=%s", why)
}

// 券低于 minQty（0.005<0.01）→ 持仓闸拒（判据下限生效）。
func TestD646_BelowMinQtyBlocks(t *testing.T) {
	st, ctx := d646Store(t)
	d646Cons(t, st, ctx)
	d646Pool(t, st, ctx, 0.005)
	d646Quote(t, st, ctx, 0.30)
	if ok, why, _ := sellHighGate(ctx, st, "okx"); ok {
		t.Fatalf("券 0.005<0.01 应拒: %s", why)
	}
}

// 判据落表：sell_high_min_qty=0.001 覆写后 0.005 券放行（热读生效断言）。
func TestD646_MinQtyTableOverride(t *testing.T) {
	st, ctx := d646Store(t)
	d646Cons(t, st, ctx)
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO app_config (key, value) VALUES ('sell_high_min_qty','0.001')`); err != nil {
		t.Fatal(err)
	}
	d646Pool(t, st, ctx, 0.005)
	d646Quote(t, st, ctx, 0.30)
	if ok, why, _ := sellHighGate(ctx, st, "okx"); !ok {
		t.Fatalf("表控 minQty=0.001 后 0.005 券应放行: %s", why)
	}
}

// 券足但无达标档（报价缺）→ 结构闸拒。
func TestD646_NoStructQuoteBlocks(t *testing.T) {
	st, ctx := d646Store(t)
	d646Cons(t, st, ctx)
	d646Pool(t, st, ctx, 0.5)
	if ok, why, _ := sellHighGate(ctx, st, "okx"); ok {
		t.Fatalf("无报价档应拒: %s", why)
	}
}

// venue 键缺→全局 custody_share 回落（D-644 向后兼容钉：无 venue 键不
// fail-closed）——池 0.5×全局 share（共享库实值）≥min 应放行。
func TestD646_GlobalShareFallback(t *testing.T) {
	st, ctx := d646Store(t)
	// σ 键同 1027 种子在案——只保 custody_share@okx 缺席（删自建域内行）。
	if _, err := st.Pool().Exec(ctx, `
		DELETE FROM fin_capability_constraint WHERE subject='custody_share@okx'`); err != nil {
		t.Fatal(err)
	}
	d646Pool(t, st, ctx, 0.5)
	d646Quote(t, st, ctx, 0.30)
	cons, err := st.ListCapabilityConstraints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	share := gate.CustodyShareOf(cons, "okx")
	if share == nil {
		t.Skip("共享库无全局 custody_share 行——回落语义无锚可验")
	}
	ok, why, _ := sellHighGate(ctx, st, "okx")
	want := 0.5*(*share) >= 0.01
	if ok != want {
		t.Fatalf("全局回落语义错: ok=%v want=%v why=%s", ok, want, why)
	}
}
