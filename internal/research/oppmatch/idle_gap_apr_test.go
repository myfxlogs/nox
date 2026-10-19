package oppmatch

import (
	"testing"
)

// TestIdleGapAprFloor cash_mgmt APR 下限闸：低于阈不提案只在假设层如实记录；
// 达阈照提案；无 APR 行=假设层记录缺面。
func TestIdleGapAprFloor(t *testing.T) {
	st, ctx := d514Store(t)
	for _, q := range []string{
		`DELETE FROM fin_proposal WHERE venue IN ('gate','okx')`,
		`DELETE FROM fin_opp_hypothesis WHERE category_key LIKE 'cash_mgmt:low_apr:%'`,
		`DELETE FROM fin_venue_account_snapshot WHERE venue IN ('gate','okx')`,
		`DELETE FROM fin_cex_earn_apr WHERE venue IN ('gate','okx')`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM fin_proposal WHERE venue IN ('gate','okx')`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM fin_opp_hypothesis WHERE category_key LIKE 'cash_mgmt:low_apr:%'`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM fin_venue_account_snapshot WHERE venue IN ('gate','okx')`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM fin_cex_earn_apr WHERE venue IN ('gate','okx')`)
	}()
	// gate=低 APR 0.000964（<0.005 阈）→不提案+假设行；okx=0.031 达阈→提案
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_cex_earn_apr
		(venue, ccy, product_type, apr, ts) VALUES
		('gate','USDT','flex',0.000964,now()),('okx','USDT','flex',0.031,now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_venue_account_snapshot
		(venue, venue_type, currency, balance_raw, equity_raw, fetched_at, src) VALUES
		('gate','perp','USDT',998,998,now(),'ws'),
		('okx','unified','USDT',1956,1956,now(),'ws')`); err != nil {
		t.Fatal(err)
	}
	idleGapScan(ctx, st)
	var gateP, okxP, gateH, okxH int
	for _, q := range []struct {
		dst *int
		sql string
	}{
		{&gateP, `SELECT count(*) FROM fin_proposal WHERE template_id='cash_mgmt' AND venue='gate'`},
		{&okxP, `SELECT count(*) FROM fin_proposal WHERE template_id='cash_mgmt' AND venue='okx' AND status='proposed'`},
		{&gateH, `SELECT count(*) FROM fin_opp_hypothesis WHERE category_key='cash_mgmt:low_apr:gate'`},
		{&okxH, `SELECT count(*) FROM fin_opp_hypothesis WHERE category_key='cash_mgmt:low_apr:okx'`},
	} {
		if err := st.Pool().QueryRow(ctx, q.sql).Scan(q.dst); err != nil {
			t.Fatal(err)
		}
	}
	if gateP != 0 {
		t.Fatalf("低 APR 不应提案: gate=%d", gateP)
	}
	if gateH != 1 {
		t.Fatalf("低 APR 应假设层留痕: gateH=%d", gateH)
	}
	if okxP != 1 || okxH != 0 {
		t.Fatalf("达阈应提案无假设行: okxP=%d okxH=%d", okxP, okxH)
	}
}
