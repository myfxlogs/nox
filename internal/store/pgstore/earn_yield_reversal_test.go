// earn_yield_reversal_test.go —— 迁移 1046 冲正行为对抗锚：SG 10-17 实拍镜像
// fixture（扇出族三批次+BTC 份额变体+USDT 本金往返三笔+须幸存真收益行）→
// 断言冲正集精确、镜像额=原额取负、幂等重跑零新增。
package pgstore

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"nox/internal/platform/testutil"
	"nox/migrations"
)

func TestEarnYieldFanoutReversal(t *testing.T) {
	admin := adminDSN(t)
	ctx := context.Background()
	db := "nox_earnrev_test_" + testutil.SeatTag()
	if out, err := exec.CommandContext(ctx, "psql", admin, "-c",
		fmt.Sprintf("DROP DATABASE IF EXISTS %s;", db)).CombinedOutput(); err != nil {
		t.Fatalf("drop old db: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		exec.CommandContext(context.Background(), "psql", admin, "-c",
			fmt.Sprintf("DROP DATABASE IF EXISTS %s;", db)).Run()
	})
	if out, err := exec.CommandContext(ctx, "psql", admin, "-c",
		fmt.Sprintf("CREATE DATABASE %s;", db)).CombinedOutput(); err != nil {
		t.Fatalf("create db: %v\n%s", err, out)
	}
	s, err := New(ctx, swapDBName(testutil.TestDSN(t, "NOX_TEST_PG_DSN"), db))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// —— fixture：SG 实拍镜像（时间戳=取证原值截秒；金额=实拍值）——
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\n%s", err, sql)
		}
	}
	must(`INSERT INTO app_config (key, value) VALUES ('ownership_share:binance:earn:BTC','0.3334')
	      ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`)
	d := func(s string) time.Time { ts, _ := time.Parse(time.RFC3339, s); return ts }
	// 申购单：#32 351.64289268（10-16 04:43）/#33 0.05479（10-16 06:25）/#34 0.05479（10-17 06:00）
	for _, o := range []struct {
		id  int
		qty float64
		at  string
	}{
		{32, 351.64289268, "2026-10-16T04:43:07Z"}, {33, 0.05479, "2026-10-16T06:25:36Z"}, {34, 0.05479, "2026-10-17T06:00:09Z"},
	} {
		must(`INSERT INTO fin_dci_order (id, request_id, venue, underlying, opt_type, invest_ccy, qty, status, created_at, resolved_at)
			  VALUES ($1,$2,'binance','USDT','EARN','USDT',$3,'filled',$4,$4)`, o.id, fmt.Sprintf("t|%d", o.id), o.qty, d(o.at))
	}
	// 快照前值链：T0=04:42（05:52 批前值）→T1=05:52（06:35 批前值）→T2=06:35；
	// 10-17 批（07:22）前值=T3=05:00（<#34，故窗含 #34——与实拍窗一致）。
	for _, c := range []string{"USDT", "DOGE", "BTC", "PEPE"} {
		for _, p := range []struct {
			at string
			eq float64
		}{
			{"2026-10-16T04:42:12Z", 1000.0}, {"2026-10-16T05:52:12Z", 1000.0}, {"2026-10-16T06:35:49Z", 1000.0},
			{"2026-10-17T05:00:00Z", 1000.0},
		} {
			must(`INSERT INTO fin_venue_account_snapshot (venue, venue_type, currency, equity_raw, fetched_at, src)
				  VALUES ('binance','earn',$1,$2,$3,'rest_poll')`, c, p.eq, d(p.at))
		}
	}
	// income 行：venue_type='earn'，cfg=binance:earn config id。
	var cfgID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM exchange_configs WHERE venue='binance' AND venue_type='earn'`).Scan(&cfgID); err != nil {
		t.Fatalf("cfg id: %v", err)
	}
	// kind: fan=扇出须冲正 / keep=真收益须幸存。tri=USDT 三笔伪影（id=SG 取证锚）。
	type ev struct {
		id   int
		sym  string
		amt  float64
		tran int64
		at   string
		kind string
	}
	evs := []ev{
		// 05:52 批（#32 窗）
		{101, "USDT", 0.0053, 900101, "2026-10-16T05:52:12Z", "keep"},
		{102, "DOGE", -351.642893, 900102, "2026-10-16T05:52:12Z", "fan"},
		{103, "BTC", -117.237740, 900103, "2026-10-16T05:52:12Z", "fan"}, // ×0.3334 份额变体
		{104, "PEPE", 0.01, 900104, "2026-10-16T05:52:12Z", "keep"},      // 批内真收益幸存
		// 06:35 批（#33 窗）
		{105, "DOGE", -0.054790, 900105, "2026-10-16T06:35:49Z", "fan"},
		{106, "BTC", -0.018267, 900106, "2026-10-16T06:35:49Z", "fan"},
		// 10-17 07:22 批（#34 窗）
		{107, "DOGE", -0.050593, 900107, "2026-10-17T07:22:21Z", "fan"}, // 真收益 +0.0042 搭行
		{108, "PEPE", -0.044790, 900108, "2026-10-17T07:22:21Z", "fan"}, // 真收益 +0.01 搭行
		{109, "BTC", -0.018267, 900109, "2026-10-17T07:22:21Z", "fan"},
		{110, "USDT", 0.0069, 900110, "2026-10-17T07:22:21Z", "keep"},
		// 批外真收益（窗内无申购）须幸存
		{111, "DOGE", 0.029660, 900111, "2026-10-15T23:41:49Z", "keep"},
		{112, "USDT", 0.0338, 900112, "2026-10-15T23:41:49Z", "keep"},
		// USDT 本金往返伪影三笔（SG id 189/240/347）
		{189, "USDT", 1799.0018, 900189, "2026-10-15T05:56:05Z", "tri"},
		{240, "USDT", -1440.9980, 900240, "2026-10-15T06:30:02Z", "tri"},
		{347, "USDT", -357.9951, 900347, "2026-10-15T08:18:01Z", "tri"},
	}
	for _, e := range evs {
		must(`INSERT INTO income_events (id, venue, venue_type, exchange_config_id, symbol, income_type, amount_usd, tran_id, ts)
			  VALUES ($1,'binance','earn',$2,$3,'earn_yield',$4,$5,$6)`, e.id, cfgID, e.sym, e.amt, e.tran, d(e.at))
	}
	must(`SELECT setval('income_events_id_seq', (SELECT max(id) FROM income_events))`)

	run := func() int64 {
		tag, err := s.pool.Exec(ctx, migrations.EarnYieldFanoutReversalSQL)
		if err != nil {
			t.Fatalf("reversal migration: %v", err)
		}
		return tag.RowsAffected()
	}
	if n := run(); n != 10 {
		t.Fatalf("冲正行数=%d want 10（扇出 7+三笔伪影 3）", n)
	}
	// 镜像逐行对拍：amount=−原额、tran_id=−原、ts=原 ts、币种=原币种。
	var mirrors []struct {
		sym  string
		amt  float64
		tran int64
		at   time.Time
	}
	rows, err := s.pool.Query(ctx, `SELECT symbol, amount_usd, tran_id, ts FROM income_events WHERE tran_id < 0 ORDER BY tran_id`)
	if err != nil {
		t.Fatalf("mirror query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m struct {
			sym  string
			amt  float64
			tran int64
			at   time.Time
		}
		if err := rows.Scan(&m.sym, &m.amt, &m.tran, &m.at); err != nil {
			t.Fatal(err)
		}
		mirrors = append(mirrors, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(mirrors) != 10 {
		t.Fatalf("镜像行=%d want 10", len(mirrors))
	}
	for _, e := range evs {
		if e.kind == "keep" {
			continue
		}
		found := false
		for _, m := range mirrors {
			if m.tran == -e.tran {
				found = true
				if m.amt != -e.amt || m.sym != e.sym || !m.at.Equal(d(e.at)) {
					t.Fatalf("镜像不忠实: %+v want(−%v,%s,%s)", m, e.amt, e.sym, e.at)
				}
			}
		}
		if !found {
			t.Fatalf("id %d (%s %v) 未冲正", e.id, e.sym, e.amt)
		}
	}
	// 幸存集逐行核对：keep 行零镜像配对。
	for _, e := range evs {
		if e.kind != "keep" {
			continue
		}
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM income_events WHERE tran_id=$1`, -e.tran).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("真收益行 id %d 被误冲正", e.id)
		}
	}
	// 幂等：重跑零新增。
	if n := run(); n != 0 {
		t.Fatalf("重跑非幂等: +%d 行", n)
	}
}
