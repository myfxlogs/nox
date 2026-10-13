package pgstore_test

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil/storetest"
)

// TestGhostPool_NAV 钦定锚：幽灵池（broker_accounts 移除户残行）剔 NAV+计数留痕；
// CEX 行（venue 不入 broker_accounts）不受影响；stopped 户在册照计=正确。
func TestGhostPool_NAV(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM fin_venue_account_snapshot WHERE venue IN ('62311223','1350684','ghostbinance')`,
		`DELETE FROM broker_accounts WHERE venue='1350684'`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
	defer func() {
		_, _ = st.Pool().Exec(ctx,
			`DELETE FROM fin_venue_account_snapshot WHERE venue IN ('62311223','1350684','ghostbinance')`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM broker_accounts WHERE venue='1350684'`)
	}()
	now := time.Now()
	// 在册 mt5 户（stopped 型态——在册即非幽灵）；幽灵 mt4 残行；CEX 池（不入 broker_accounts）。
	if _, err := st.Pool().Exec(ctx, `INSERT INTO broker_accounts
		(venue, venue_type, host, port, password, is_real) VALUES ('1350684','mt5','h',443,'p',true)`); err != nil {
		t.Fatalf("seed broker: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_venue_account_snapshot
		(venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at) VALUES
		('1350684','mt5',1000,1000,1000,'USD',$1),
		('62311223','mt4',172.95,172.95,172.95,'USD',$1),
		('ghostbinance','perp',2000,2000,2000,'USD',$1)`, now); err != nil {
		t.Fatalf("seed snapshots: %v", err)
	}
	pools, err := st.ExplorerIdleFunds(ctx, now)
	if err != nil {
		t.Fatalf("idle funds: %v", err)
	}
	var ghost, live, cex int
	for _, p := range pools {
		switch p.Venue {
		case "62311223":
			if !p.Ghost {
				t.Fatal("移除户残行应打 Ghost 标")
			}
			if p.Idle || p.BelowFloor {
				t.Fatal("幽灵池不标 Idle/BelowFloor")
			}
			ghost++
		case "1350684":
			if p.Ghost {
				t.Fatal("在册户不应剔")
			}
			live++
		case "ghostbinance":
			if p.Ghost {
				t.Fatal("CEX 行不入 broker_accounts——不受影响")
			}
			cex++
		}
	}
	if ghost != 1 || live != 1 || cex != 1 {
		t.Fatalf("池集错: ghost=%d live=%d cex=%d", ghost, live, cex)
	}
	nav, err := st.ComputeNav(ctx, now)
	if err != nil {
		t.Fatalf("ComputeNav: %v", err)
	}
	if nav.SkippedGhost < 1 {
		t.Fatalf("SkippedGhost 应计数留痕: %d", nav.SkippedGhost)
	}
}
