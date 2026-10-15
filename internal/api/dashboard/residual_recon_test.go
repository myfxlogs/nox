// residual_recon_test.go —— D-539②：已知划转场景等式收敛（零告警）+未解释残差触 critical。
package dashboard

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

func d536Store(t *testing.T) *pgstore.Store {
	t.Helper()
	s := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`TRUNCATE fin_venue_account_snapshot, fin_venue_io, income_events, alerts RESTART IDENTITY`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `DELETE FROM exchange_configs WHERE venue='d536bn'`); err != nil {
		t.Fatalf("cfg clean: %v", err)
	}
	// spot/earn 两池各一配置行（config_id 归属）。
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO exchange_configs (venue, venue_type, api_key, api_secret) VALUES
		 ('d536bn','spot','x','x'), ('d536bn','earn','x','x')`); err != nil {
		t.Fatalf("seed cfg: %v", err)
	}
	return s
}

func d536Snap(vt, ccy string, equity float64, ts time.Time) pgstore.VenueAccountSnapshot {
	return pgstore.VenueAccountSnapshot{Venue: "d536bn", VenueType: vt, Currency: ccy,
		EquityRaw: equity, FetchedAt: ts, Src: "test"}
}

func TestD539_ResidRecon(t *testing.T) {
	s := d536Store(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	// 重放场景：spot 1000 → spot 800 + earn 200（划转台账解释；等式收敛零告警）。
	cfgSpot, _ := s.ExchangeConfigID(ctx, "d536bn", "spot")
	cfgEarn, _ := s.ExchangeConfigID(ctx, "d536bn", "earn")
	for _, sn := range []pgstore.VenueAccountSnapshot{
		d536Snap("spot", "USDT", 1000, base), d536Snap("earn", "USDT", 0, base),
	} {
		if err := s.InsertVenueAccountSnapshots(ctx, []pgstore.VenueAccountSnapshot{sn}); err != nil {
			t.Fatalf("seed snap: %v", err)
		}
	}
	if _, err := s.InsertVenueIO(ctx, []pgstore.VenueIO{{
		Venue: "d536bn", ConfigID: cfgEarn, Direction: "transfer", Ccy: "USDT",
		Amount: 200, ExternalID: "se-p1", Ts: base.Add(10 * time.Minute),
		FromWallet: "spot", ToWallet: "earn",
	}}); err != nil {
		t.Fatalf("seed transfer: %v", err)
	}
	r := NewResidualRecon(s)
	now := base.Add(time.Hour)
	for _, sn := range []pgstore.VenueAccountSnapshot{
		d536Snap("spot", "USDT", 800, now), d536Snap("earn", "USDT", 200, now),
	} {
		row := sn
		if err := r.evaluate(ctx, &row); err != nil {
			t.Fatalf("eval %s: %v", row.VenueType, err)
		}
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE message LIKE '%残差对账%'`).Scan(&n); err != nil {
		t.Fatalf("alerts: %v", err)
	}
	if n != 0 {
		t.Fatalf("已解释划转不应触警: alerts=%d", n)
	}
	// 未解释残差：spot 再变 −50（无台账）→ critical。
	if err := r.evaluate(ctx, &pgstore.VenueAccountSnapshot{
		Venue: "d536bn", VenueType: "spot", Currency: "USDT",
		EquityRaw: 750, FetchedAt: now.Add(time.Minute), Src: "test",
	}); err != nil {
		t.Fatalf("eval2: %v", err)
	}
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='critical' AND message LIKE '%残差对账%'`).Scan(&n); err != nil {
		t.Fatalf("alerts2: %v", err)
	}
	if n != 1 {
		t.Fatalf("未解释残差应触 critical: alerts=%d", n)
	}
	// transfer 行的 config 归属=申购池（earn）——划转净值按 ccy+钱包配 config 读：
	// 上面 spot 池侧账平的前提是同 config 域；这里同 venue 双 config 各行归位正确即收敛。
	_ = cfgSpot
}
