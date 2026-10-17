package oppmatch

// d514_test.go —— D-514 溢价闸锚：premium_ann<max(3×earn_apr,8%) 拒产；
// 高溢价放行、报价缺/earn 缺 fail-closed；idle 缺口扫描+swap 面假设同锚。

import (
	"context"
	"strings"
	"testing"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

func d514Store(t *testing.T) (*pgstore.Store, context.Context) {
	t.Helper()
	// D-526：σ 注口小值（0.001 宽界保 fixture strike 达标、tail≈0 不挡溢价锚）。
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
		`DELETE FROM fin_cex_dci_quote WHERE product_ref LIKE 'D514%'`,
		`DELETE FROM fin_cex_earn_apr WHERE ccy LIKE 'D514%' OR (ccy='USDT' AND apr=1.08)`,
		`DELETE FROM fin_venue_account_snapshot WHERE venue LIKE 'd514%'`,
		`DELETE FROM fin_condition_gap WHERE subject LIKE 'd514%'`,
		`DELETE FROM fin_opp_hypothesis WHERE category_key LIKE 'cfd_swap|d514%'`,
		`DELETE FROM facts WHERE venue LIKE 'd514%'`,
		`DELETE FROM app_config WHERE key LIKE 'ownership_share:d514%'`,
		`DELETE FROM fin_cex_spread WHERE venue_buy LIKE 'd514%'`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	// D-526 尾险闸依赖 BTC 现货参照（LatestSpreadSpotRef）——鲜库无行必拒
	// 「BTC 现货参照缺」（CI 红病灶）；spot=90000 使 σ=0.001 界（×0.992≈89262）
	// 罩住 fixture strike=80000。venue 列打 d514 标供清理。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO fin_cex_spread (symbol, venue_buy, venue_sell, price_buy, price_sell, spread_bps)
		 VALUES ('BTC','d514a','d514b',90000,90000,0)`); err != nil {
		t.Fatal(err)
	}
	return st, ctx
}

func d514Put(t *testing.T, st *pgstore.Store, ctx context.Context, apy float64) {
	t.Helper()
	tenor := 7
	strike := 80000.0
	if err := st.InsertCexDCIQuote(ctx, &pgstore.CexDCIQuote{
		Venue: "binance", Underlying: "BTC", InvestCcy: "USDT", OptType: "PUT",
		Strike: &strike, TenorDays: &tenor, APY: apy, ProductRef: "D514P",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestD514_PremiumGate(t *testing.T) {
	st, ctx := d514Store(t)
	// 闸值由共享库 earn 现值实算（同币种 USDT 基准口径——不测固定值，测关系式）。
	// 异币种营销档在场不入基准（BETH 365% 型病灶锚）。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO fin_cex_earn_apr (venue, ccy, product_type, apr, ts)
		 VALUES ('okx','D514MOON','earn',365.0, now())`); err != nil {
		t.Fatal(err)
	}
	_, earn, _, _, err := st.MaxEarnAPRByCcy(ctx, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	thr := 0.08
	if 3*earn > thr {
		thr = 3 * earn
	}
	if thr > 0.5 { // 锚前照：异币种行销档若混入基准则 thr 虚高——钉死口径。
		t.Fatalf("USDT 基准被异币种污染: thr=%v", thr)
	}
	// 鲜度闸锚：陈旧 USDT 高 APR 行（>24h）不入基准——现值才是真替代。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO fin_cex_earn_apr (venue, ccy, product_type, apr, ts)
		 VALUES ('okx','USDT','earn',1.08, now()-interval '2 days')`); err != nil {
		t.Fatal(err)
	}
	if bv, ba, _, _, err := st.MaxEarnAPRByCcy(ctx, "USDT"); err != nil || ba >= 1.0 {
		t.Fatalf("陈旧行销档混入基准: %s %.4f %v", bv, ba, err)
	}
	// 高溢价（闸×1.5；apy 列=binance %口径——闸小数×100 入库）→放行。
	d514Put(t, st, ctx, thr*150)
	if ok, why := dciPremiumGate(ctx, st, "binance"); !ok {
		t.Fatalf("高溢价应放行: %s", why)
	}
	// 低溢价（闸×0.5）→拒「溢价低于闸」。
	if _, err := st.Pool().Exec(ctx,
		`UPDATE fin_cex_dci_quote SET apy=$1 WHERE product_ref='D514P'`, thr*50); err != nil {
		t.Fatal(err)
	}
	if ok, why := dciPremiumGate(ctx, st, "binance"); ok || !strings.Contains(why, "溢价低于闸") {
		t.Fatalf("低溢价应拒: ok=%v why=%s", ok, why)
	}
	// 报价缺→fail-closed 拒。
	if _, err := st.Pool().Exec(ctx, `DELETE FROM fin_cex_dci_quote WHERE product_ref='D514P'`); err != nil {
		t.Fatal(err)
	}
	if ok, why := dciPremiumGate(ctx, st, "binance"); ok || !strings.Contains(why, "报价缺") {
		t.Fatalf("报价缺应 fail-closed 拒: ok=%v why=%s", ok, why)
	}
}

func TestD514_IdleGapScan(t *testing.T) {
	st, ctx := d514Store(t)
	// okx unified 闲置池（非 earn 执行径 venue——余额>0 且 30d 零收入=Idle）。
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO fin_venue_account_snapshot (venue, venue_type, currency, balance_raw, equity_raw, fetched_at, src)
		VALUES ('d514okx','unified','USDT',1956,1956,now(),'ws')`); err != nil {
		t.Fatal(err)
	}
	idleGapScan(ctx, st)
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_condition_gap WHERE template_id='cash_mgmt' AND kind='executor' AND subject='d514okx:unified'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("idle 缺口行未落: %d", n)
	}
	// 重跑幂等零重行。
	idleGapScan(ctx, st)
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_condition_gap WHERE subject='d514okx:unified'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("重跑重推行: %d", n)
	}
}

func TestD514_SwapScan(t *testing.T) {
	st, ctx := d514Store(t)
	// D-604 P1②：权益=判定拍实拉（BindMTEquityFn 注入；快照面退役）——
	// d514m1 户有权益；d514m0 户 equity=0（面在钱不入榜，fail-closed 语义同）。
	BindMTEquityFn(func(venue string) (float64, bool) {
		if venue == "d514m1" {
			return 3200, true
		}
		return 0, false
	})
	t.Cleanup(func() { BindMTEquityFn(nil) })
	for _, q := range []string{
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES
		 ('swap_long','d514m1','BRENT',1.46,'bps_daily',now(),'mt5'),
		 ('swap_long','d514m0','NAS',0.25,'bps_daily',now(),'mt5'),
		 ('swap_short','d514m1','WTI',-0.5,'bps_daily',now(),'mt5')`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	swapScan(ctx, st)
	var n int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_opp_hypothesis WHERE category_key='cfd_swap|d514m1|BRENT|long'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("正息面假设行未落: %d", n)
	}
	// 权益 0 户不入榜；负息面不入榜。
	for _, k := range []string{"cfd_swap|d514m0|NAS|long", "cfd_swap|d514m1|WTI|short"} {
		if err := st.Pool().QueryRow(ctx,
			`SELECT count(*) FROM fin_opp_hypothesis WHERE category_key=$1`, k).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("域外假设行误落: %s", k)
		}
	}
	// 幂等：重跑零重推（行仍在册不新增）。
	swapScan(ctx, st)
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_opp_hypothesis WHERE category_key LIKE 'cfd_swap|d514%'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("重跑重复落行: %d", n)
	}
}
