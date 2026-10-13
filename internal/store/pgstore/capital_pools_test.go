package pgstore

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestCapitalPools_Store 资金地图读面三源锚（共享测试库，本测试独占三表）。
func TestCapitalPools_Store(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, q := range []string{
		`TRUNCATE fin_venue_account_snapshot CASCADE`,
		`TRUNCATE broker_accounts CASCADE`,
		`TRUNCATE fin_dci_order CASCADE`,
		`TRUNCATE positions, position_legs CASCADE`,
	} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	fresh := time.Now().Add(-time.Minute)
	old := time.Now().Add(-time.Hour)
	ins := `INSERT INTO fin_venue_account_snapshot (venue, venue_type, equity_raw, free_margin_raw, currency, src, fetched_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`
	for _, r := range [][7]interface{}{
		{"capmt90001", "mt5", 5000.0, 4500.0, "USD", "mt_frame", fresh},
		{"capmtghost", "mt5", 172.0, 172.0, "USD", "mt_frame", old},
		{"capcex", "spot", 1045.0, 0.0, "USDT", "capcex_event", fresh},
		{"capcex", "earn", 169.0, 0.0, "USDT", "rest_poll", fresh},
	} {
		if _, err := s.pool.Exec(ctx, ins, r[0], r[1], r[2], r[3], r[4], r[5], r[6]); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, commission_bps, is_real, website)
		 VALUES ('capmt90001','mt5','h',0,'','s','active',0,true,'')`); err != nil {
		t.Fatalf("broker: %v", err)
	}
	// 未到期 DCI：settleDate 回执 now+2d 毫秒戳 → outstanding。
	settle := time.Now().Add(48 * time.Hour).UnixMilli()
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO fin_dci_order (venue, underlying, invest_ccy, qty, tenor_days, status, response, request_id)
		VALUES ('capcex','BTC','USDT',$1,7,'filled',$2,'captest-req-1')`,
		542.0, fmt.Sprintf(`{"settleDate":"%d"}`, settle)); err != nil {
		t.Fatalf("dci: %v", err)
	}
	// OPEN 持仓：capcex:perp 腿名义 10×100=1000。
	var pid int64
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at)
		 VALUES ('captest-opp','PERP_FUNDING','OPEN',now()) RETURNING id`).Scan(&pid); err != nil {
		t.Fatalf("position: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status, filled_qty, avg_fill_price)
		VALUES ($1,1,'OPEN','capcex','perp','BTCUSDT','BUY',10,100,'FILLED',10,100)`, pid); err != nil {
		t.Fatalf("leg: %v", err)
	}

	pools, err := s.CapitalPoolSnapshots(ctx)
	if err != nil {
		t.Fatalf("snapshots: %v", err)
	}
	got := map[string]CapitalPoolRow{}
	for _, p := range pools {
		got[p.Venue+":"+p.VenueType] = p
	}
	if got["capmt90001:mt5"].FetchedAt.Sub(fresh).Abs() > time.Second || got["capmt90001:mt5"].Ghost {
		t.Errorf("mt5 在册池: %+v", got["capmt90001:mt5"])
	}
	if !got["capmtghost:mt5"].Ghost {
		t.Error("移除户残行应标 ghost")
	}
	if got["capcex:spot"].Ghost {
		t.Error("CEX 池不套 ghost 语义")
	}

	dcis, err := s.CapitalOutstandingDCI(ctx)
	if err != nil {
		t.Fatalf("dci: %v", err)
	}
	if len(dcis) != 1 || dcis[0].Qty != 542 || dcis[0].CCY != "USDT" {
		t.Errorf("outstanding dci: %+v", dcis)
	}

	legs, err := s.CapitalOpenLegs(ctx)
	if err != nil {
		t.Fatalf("legs: %v", err)
	}
	if len(legs) != 1 || legs[0].VenueKey != "capcex:perp" || legs[0].Amount != 1000 || legs[0].PosType != "PERP_FUNDING" {
		t.Errorf("open legs: %+v", legs)
	}
}
