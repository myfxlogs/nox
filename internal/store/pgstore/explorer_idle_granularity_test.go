package pgstore_test

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil/storetest"
)

// TestIdleGranularity_VenueKeyIncome 钦定锚（okx idle 粒度修票）：收入判据下沉
// venue_key 粒度——「okx:perp 仓位收入」不得掩盖「okx:unified 躺账现金」；
// venue_type=” 裸键收入行不可归因→并入同 venue 各池保守判闲（旧语义保）。
func TestIdleGranularity_VenueKeyIncome(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM fin_venue_account_snapshot WHERE venue LIKE 'xgokx%'`,
		`DELETE FROM income_events WHERE venue LIKE 'xgokx%'`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
	defer func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM fin_venue_account_snapshot WHERE venue LIKE 'xgokx%'`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM income_events WHERE venue LIKE 'xgokx%'`)
	}()
	now := time.Now()
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_venue_account_snapshot
		(venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at) VALUES
		('xgokx','unified',1956,1956,1956,'USDT',$1),
		('xgokx','perp',500,500,500,'USDT',$1),
		('xgbn','earn',800,800,800,'USDT',$1)`, now); err != nil {
		t.Fatalf("seed snapshots: %v", err)
	}
	// perp 腿仓位收入（venue_key='xgokx:perp'）+ 裸键收入（venue_type='' 归 xgbn）。
	if _, err := st.Pool().Exec(ctx, `INSERT INTO income_events
		(venue, venue_type, symbol, income_type, amount_usd, tran_id, ts) VALUES
		('xgokx','perp','XAUT-USDT-SWAP','FUNDING_FEE',4.31,990001,$1),
		('xgbn','','BNB','SAVINGS',1.0,990002,$1)`, now.Add(-time.Hour)); err != nil {
		t.Fatalf("seed income: %v", err)
	}
	pools, err := st.ExplorerIdleFunds(ctx, now)
	if err != nil {
		t.Fatalf("ExplorerIdleFunds: %v", err)
	}
	got := map[string]bool{}
	for _, p := range pools {
		if p.Venue == "xgokx" || p.Venue == "xgbn" {
			got[p.Venue+":"+p.VenueType] = p.Idle
		}
	}
	if !got["xgokx:unified"] {
		t.Fatal("xgokx:unified 现金 $1956 无归因收入应 Idle=true（perp 仓位收入不得掩盖）")
	}
	if got["xgokx:perp"] {
		t.Fatal("xgokx:perp 有 venue_key 归因收入应 Idle=false")
	}
	if got["xgbn:earn"] {
		t.Fatal("xgbn 裸键收入不可归因→earn 池保守 Idle=false")
	}
}
