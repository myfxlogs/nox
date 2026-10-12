// d152_retention_test.go —— D-152：六表 TTL + cex_private_events 分档锚测（DSN 门控）。
// 每表三类行：超窗非终值（删）/窗内（留）/组终值（超窗但组内最新=卫兵留）。
package pgstore

import (
	"context"
	"testing"
	"time"
)

// d152Count 表行数断言小助。
func d152Count(t *testing.T, ctx context.Context, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestD152_PruneRetentionTables(t *testing.T) {
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()
	now := time.Now()
	old := now.Add(-100 * 24 * time.Hour) // 超窗
	mid := now.Add(-20 * 24 * time.Hour)  // 窗内
	for _, ddl := range []string{
		`TRUNCATE fin_cex_radar, fin_xasset_monitor, fin_venue_account_snapshot,
			fin_symdig_findings, fin_trx_account`,
	} {
		if _, err := s.pool.Exec(ctx, ddl); err != nil {
			t.Fatalf("truncate: %v", err)
		}
	}
	// fin_cex_radar（14d 窗；组=venue+symbol）：超窗非终值删/窗内留/超窗终值卫兵留
	if _, err := s.pool.Exec(ctx, `INSERT INTO fin_cex_radar (ts,venue,symbol) VALUES
		($1,'va','OLD'),($2,'va','OLD'),($3,'va','NEW'),($4,'vb','TERM')`,
		old, mid, mid, old); err != nil {
		t.Fatal(err)
	}
	// fin_xasset_monitor（90d；组=pair+cex_venue+mt_venue）
	if _, err := s.pool.Exec(ctx, `INSERT INTO fin_xasset_monitor
		(ts,pair,cex_venue,mt_venue,binance_bid,binance_ask,exness_bid,exness_ask,
		 entry_spread_bps,exit_spread_bps,entry_net_bps,funding_rate_8h,funding_annual_pct,
		 tracking_error_bps,equiv_annual_pct) VALUES
		($1,'P','binance:perp','3001157:mt5',1,1,1,1,0,0,0,0,0,0,0),
		($2,'P','binance:perp','3001157:mt5',1,1,1,1,0,0,0,0,0,0,0),
		($3,'Q','okx:perp','3001157:mt5',1,1,1,1,0,0,0,0,0,0,0)`,
		old, mid, old); err != nil {
		t.Fatal(err)
	}
	// fin_venue_account_snapshot（90d；组=venue+venue_type）
	if _, err := s.pool.Exec(ctx, `INSERT INTO fin_venue_account_snapshot
		(venue,venue_type,balance_raw,fetched_at) VALUES
		('binance','spot',1,$1),('binance','spot',2,$2),('okx','earn',3,$3)`,
		old, mid, old); err != nil {
		t.Fatal(err)
	}
	// fin_symdig_findings（90d；组=venue+symbol+klass）
	if _, err := s.pool.Exec(ctx, `INSERT INTO fin_symdig_findings
		(run_ts,venue,symbol,klass) VALUES
		($1,'va','S1','unpaired'),($2,'va','S1','unpaired'),($3,'va','S2','paired')`,
		old, mid, old); err != nil {
		t.Fatal(err)
	}
	// fin_trx_account（90d；组=address）
	if _, err := s.pool.Exec(ctx, `INSERT INTO fin_trx_account
		(address,votes_total,balance_sun,allowance_sun,energy_delegated,bandwidth_staked,net_limit,fetched_at)
		VALUES ('A1',0,0,0,0,0,0,$1),('A1',0,0,0,0,0,0,$2),('A2',0,0,0,0,0,0,$3)`,
		old, mid, old); err != nil {
		t.Fatal(err)
	}
	if err := s.PruneCexRadar(ctx, 14); err != nil {
		t.Fatalf("radar prune: %v", err)
	}
	got, err := s.PruneRetentionTables(ctx)
	if err != nil {
		t.Fatalf("prune tables: %v", err)
	}
	// 每表删 1（超窗非终值），留 2（窗内+组终值卫兵）
	for tbl, want := range map[string]int{
		"fin_cex_radar": 3, "fin_xasset_monitor": 2, "fin_venue_account_snapshot": 2,
		"fin_symdig_findings": 2, "fin_trx_account": 2,
	} {
		if n := d152Count(t, ctx, s, tbl); n != want {
			t.Fatalf("%s 留存 want=%d got=%d (pruned=%v)", tbl, want, n, got)
		}
	}
	// 幂等：二轮零删
	got, err = s.PruneRetentionTables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for tbl, n := range got {
		if n != 0 {
			t.Fatalf("%s 二轮应零删 got=%d", tbl, n)
		}
	}
}

func TestD152_PruneCexPrivateEvents(t *testing.T) {
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE cex_private_events`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	seed := func(ch string, payload string, age time.Duration) {
		if _, err := s.pool.Exec(ctx, `INSERT INTO cex_private_events
			(venue,venue_type,channel,event_key,payload,received_at) VALUES
			('okx','perp',$1,'',$2::jsonb,$3)`, ch, payload, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	// 心跳帧（positions 无归零项）10d → 删
	seed("positions", `{"data":[{"instId":"X","pos":"5"}]}`, 10*24*time.Hour)
	// 证据帧（positions 含 pos=0）10d → 留（90d 档）
	seed("positions", `{"data":[{"instId":"X","pos":"0"},{"instId":"Y","pos":"2"}]}`, 10*24*time.Hour)
	// binance account 帧 pa=0 证据 10d → 留
	seed("account", `{"P":[{"s":"BTC","pa":"0"}]}`, 10*24*time.Hour)
	// binance account 心跳 pa>0 10d → 删
	seed("account", `{"P":[{"s":"BTC","pa":"3"}]}`, 10*24*time.Hour)
	// 心跳 3d 窗内 → 留
	seed("positions", `{"data":[{"instId":"X","pos":"5"}]}`, 3*24*time.Hour)
	// 证据帧 100d 超 90d 顶格 → 删
	seed("positions", `{"data":[{"instId":"X","pos":"0"}]}`, 100*24*time.Hour)
	// 杂频道 10d → 留（90d 档）；杂频道 100d → 删
	seed("futures.usertrades", `[{"contract":"X","size":-1}]`, 10*24*time.Hour)
	seed("futures.usertrades", `[{"contract":"X","size":-1}]`, 100*24*time.Hour)

	n, err := s.PruneCexPrivateEvents(ctx)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 4 {
		t.Fatalf("删行数 want=4 got=%d", n)
	}
	if cnt := d152Count(t, ctx, s, "cex_private_events"); cnt != 4 {
		t.Fatalf("留存 want=4 got=%d", cnt)
	}
	// 留存构成核验：2 证据帧 + 窗内心跳 + 窗内杂频道
	var evid int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM cex_private_events
		WHERE channel='positions' AND received_at < $1`, now.Add(-8*24*time.Hour)).Scan(&evid); err != nil {
		t.Fatal(err)
	}
	if evid != 1 {
		t.Fatalf("10d 档应只留证据帧 1 行, got=%d", evid)
	}
	// 幂等二轮零删
	if n, err := s.PruneCexPrivateEvents(ctx); err != nil || n != 0 {
		t.Fatalf("二轮应零删 n=%d err=%v", n, err)
	}
}
