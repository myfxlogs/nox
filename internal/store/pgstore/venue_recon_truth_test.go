package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestTrueNetProfitSince D-630 权威口径锚：种子权益差−io 净流——
// venue 快照差 100、io 入金 40 → 真盈亏=60；缺基线池落 Uncovered 不冒零。
func TestTrueNetProfitSince(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	base := time.Now().Add(-48 * time.Hour)
	now := time.Now()
	for _, x := range []struct {
		q string
		a []any
	}{
		{`DELETE FROM fin_venue_account_snapshot WHERE venue='truthtest'`, nil},
		{`DELETE FROM fin_venue_io WHERE venue='truthtest'`, nil},
		{`INSERT INTO fin_venue_account_snapshot (venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at)
		  VALUES ('truthtest','spot',1000,1000,1000,'USDT',$1)`, []any{base.Add(-time.Hour)}},
		{`INSERT INTO fin_venue_account_snapshot (venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at)
		  VALUES ('truthtest','spot',1100,1100,1100,'USDT',$1)`, []any{now.Add(-time.Minute)}},
		{`INSERT INTO fin_venue_io (venue, config_id, direction, ccy, amount, status, external_id, ts)
		  VALUES ('truthtest', 1, 'deposit','USDT',40,'OK','dep-1',$1)`, []any{base.Add(time.Hour)}},
	} {
		if _, err := s.pool.Exec(ctx, x.q, x.a...); err != nil {
			t.Fatalf("%s: %v", x.q[:30], err)
		}
	}
	r, err := s.TrueNetProfitSince(ctx, base.Add(-time.Minute*30), now)
	if err != nil {
		t.Fatal(err)
	}
	// Δequity=+100 − io 入金 40 = 真盈 60
	if r.NetUSD < 59.99 || r.NetUSD > 60.01 {
		t.Fatalf("权威净盈亏应=60, got=%.4f covered=%v", r.NetUSD, r.Covered)
	}
	// 缺基线池如实标：since 早界早于所有快照→无基线拍=Uncovered
	r2, err := s.TrueNetProfitSince(ctx, base.Add(-2*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range r2.Uncovered {
		if u == "truthtest|spot|USDT" {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺基线池应入 Uncovered: %v", r2.Uncovered)
	}
}
