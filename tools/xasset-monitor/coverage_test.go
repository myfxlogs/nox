package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// TestCoverageAudit D-625 P1 两型锚：24h 行数<12→饥饿 warn；最新行龄>15min
// →断供 warn（各末行实施席令）。
func TestCoverageAudit(t *testing.T) {
	s := d335r1Store(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `DELETE FROM alerts WHERE message LIKE '数据覆盖%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `DELETE FROM fin_xasset_monitor WHERE pair='COVTEST'`); err != nil {
		t.Fatal(err)
	}
	// 饥饿对：3 行陈旧（>15min）→两型各一发。
	for i := 0; i < 3; i++ {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO fin_xasset_monitor (pair, cex_venue, mt_venue, binance_bid, binance_ask, exness_bid, exness_ask,
				entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h, funding_annual_pct,
				tracking_error_bps, equiv_annual_pct, noise_bps, harvest_floor_bps, ts)
		VALUES ('COVTEST','binance:perp','3001157:mt5',4355,4356,4354,4355,12,5,3,0.001,21.9,1.0,24.9,9,7,$1)`,
			time.Now().Add(-time.Duration(60+i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	coverageAudit(ctx, s)
	var hungry, stale bool
	rows, err := s.Pool().Query(ctx, `SELECT message FROM alerts WHERE message LIKE '数据覆盖%COVTEST%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var m string
		if rows.Scan(&m) != nil {
			continue
		}
		if strings.Contains(m, "饥饿") && strings.Contains(m, "[角色:实施]") {
			hungry = true
		}
		if strings.Contains(m, "断供") && strings.Contains(m, "[角色:实施]") {
			stale = true
		}
	}
	if !hungry || !stale {
		t.Fatalf("两型告警缺一: hungry=%v stale=%v", hungry, stale)
	}
	var _ = pgstore.XAssetCoverageRow{}
}
