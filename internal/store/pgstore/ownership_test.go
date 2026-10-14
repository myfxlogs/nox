package pgstore

// ownership_test.go —— D-516 共享持有归属锚：app_config 系数→池读径乘系+
// 代管余量透出；缺键=1.0 全归属；越界配置不计。

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil"
)

func TestD516_OwnershipShare(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DELETE FROM fin_venue_account_snapshot WHERE venue='d516bn'`,
		`DELETE FROM app_config WHERE key LIKE 'ownership_share:d516%'`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO fin_venue_account_snapshot (venue, venue_type, currency, balance_raw, equity_raw, fetched_at, src)
		VALUES ('d516bn','earn','BTC',0.0568,0.0568,now(),'scheduler')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO app_config (key, value) VALUES ('ownership_share:d516bn:earn:BTC','0.3334')`); err != nil {
		t.Fatal(err)
	}
	// 越界/坏值配置不计（不放大资产）。
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO app_config (key, value) VALUES ('ownership_share:d516bn:earn:ETH','1.5')`); err != nil {
		t.Fatal(err)
	}

	pools, err := st.CapitalPoolSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range pools {
		if p.Venue != "d516bn" || p.VenueType != "earn" || p.Currency != "BTC" {
			continue
		}
		found = true
		want := 0.0568 * 0.3334
		if p.Equity < want-1e-9 || p.Equity > want+1e-9 {
			t.Fatalf("归属份对拍败: %v want %v", p.Equity, want)
		}
		if p.Custodial < 0.0568*(1-0.3334)-1e-9 {
			t.Fatalf("代管余量对拍败: %v", p.Custodial)
		}
		if p.Share != 0.3334 {
			t.Fatalf("share 透出对拍败: %v", p.Share)
		}
	}
	if !found {
		t.Fatal("d516 池行缺")
	}

	// ExplorerIdleFunds 同口径乘系（NAV 分母源）。
	idles, err := st.ExplorerIdleFunds(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range idles {
		if p.Venue == "d516bn" {
			want := 0.0568 * 0.3334
			if p.Equity < want-1e-9 || p.Equity > want+1e-9 {
				t.Fatalf("idle 面归属份对拍败: %v want %v", p.Equity, want)
			}
		}
	}

	// 撤键→全归属回复（默认 1.0 行为零变）。
	if _, err := st.Pool().Exec(ctx,
		`DELETE FROM app_config WHERE key='ownership_share:d516bn:earn:BTC'`); err != nil {
		t.Fatal(err)
	}
	pools2, err := st.CapitalPoolSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pools2 {
		if p.Venue == "d516bn" && p.VenueType == "earn" {
			if p.Equity != 0.0568 || p.Share != 1.0 || p.Custodial != 0 {
				t.Fatalf("撤键全归属对拍败: equity=%v share=%v custodial=%v",
					p.Equity, p.Share, p.Custodial)
			}
		}
	}
}
