package oppmatch

import (
	"testing"
)

// TestCashMgmtKeepalive 返修 a 存活锚：idleGapScan 产的 pending cash_mgmt
// 提案跨两次 RunOnce 不被 ExpireProposalsExcept 误杀（liveTmpls 保活面
// 与产制解耦——剔出 continue 伤保活面的回归锁）。
// 夹具：gate 池全清再种（快照+达阈 APR+清 income——共享库残留隔离）。
func TestCashMgmtKeepalive(t *testing.T) {
	st, ctx := d514Store(t)
	seedTmpl(t, st, "cash_mgmt", "yield_on_holdings", true, true)
	for _, q := range []string{
		`DELETE FROM fin_proposal WHERE template_id='cash_mgmt'`,
		`DELETE FROM fin_venue_account_snapshot WHERE venue='gate'`,
		`DELETE FROM fin_cex_earn_apr WHERE venue='gate'`,
		`DELETE FROM income_events WHERE venue='gate'`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	seedLive := func() {
		if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_cex_earn_apr
			(venue, ccy, product_type, apr, ts) VALUES ('gate','USDT','flex',0.031,now());
			INSERT INTO fin_venue_account_snapshot
			(venue, venue_type, currency, balance_raw, equity_raw, fetched_at, src)
			VALUES ('gate','perp','USDT',998,998,now(),'ws')`); err != nil {
			t.Fatal(err)
		}
	}
	seedLive()
	var pid int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO fin_proposal (template_id, exec_track, venue, status)
		 VALUES ('cash_mgmt','auto','gate','proposed') RETURNING id`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		seedLive() // 每扫前续鲜——快照时戳不随拍退化为非最新行
		if _, _, err := RunOnce(ctx, st, 10000, 1000); err != nil {
			t.Fatalf("RunOnce %d: %v", i, err)
		}
	}
	var status string
	var id2 int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT status,id FROM fin_proposal WHERE template_id='cash_mgmt' AND venue='gate' ORDER BY id DESC LIMIT 1`).Scan(&status, &id2); err != nil {
		t.Fatal(err)
	}
	if status != "proposed" || id2 != pid {
		t.Fatalf("跨两扫应存活同 id: status=%s id=%d want=%d", status, id2, pid)
	}
}
