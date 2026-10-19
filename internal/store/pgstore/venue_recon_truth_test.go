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
	// 权威等式对拍：NetUSD = Σ池 Δequity − Σ窗内 io 净流（同窗自读对拍——
	// 测试库共享面，Δequity 本测试仅 truthtest 池盖）。io 净流直接同窗读。
	var ioNet float64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(CASE direction
		WHEN 'deposit' THEN amount WHEN 'withdraw' THEN -amount ELSE 0 END),0)
		FROM fin_venue_io WHERE ts>$1 AND ts<=$2 AND direction IN ('deposit','withdraw')`,
		base.Add(-time.Minute*30), now).Scan(&ioNet); err != nil {
		t.Fatal(err)
	}
	if want := 100.0 - ioNet; r.NetUSD < want-0.01 || r.NetUSD > want+0.01 {
		t.Fatalf("权威净盈亏应=Δ100−io%.4f=%.4f, got=%.4f covered=%v", ioNet, want, r.NetUSD, r.Covered)
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

// TestTrueNetProfitSince_PerpMt5Covered 返修①对抗锚：种 perp/mt5 池行断言
// 入 Covered（返修前 venue_type 白名单只 spot/funding/earn=57% 本金盲面）。
func TestTrueNetProfitSince_PerpMt5Covered(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	base := time.Now().Add(-48 * time.Hour)
	now := time.Now()
	for _, x := range []struct {
		vt, ccy  string
		eq0, eq1 float64
	}{
		{"perp", "USDT", 500, 520},    // binance:perp 域
		{"mt5", "USD", 800, 810},      // MT5 户域
		{"unified", "USDT", 300, 290}, // okx unified 域
	} {
		vn := "truth-" + x.vt
		for _, q := range [][2]any{
			{`DELETE FROM fin_venue_account_snapshot WHERE venue=$1 AND venue_type=$2`, nil},
		} {
			_ = q
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM fin_venue_account_snapshot WHERE venue=$1`, vn); err != nil {
			t.Fatal(err)
		}
		for _, row := range []struct {
			eq float64
			ts time.Time
		}{{x.eq0, base.Add(-time.Hour)}, {x.eq1, now.Add(-time.Minute)}} {
			if _, err := s.pool.Exec(ctx,
				`INSERT INTO fin_venue_account_snapshot (venue,venue_type,balance_raw,equity_raw,free_margin_raw,currency,fetched_at)
				 VALUES ($1,$2,$3,$3,$3,$4,$5)`, vn, x.vt, row.eq, x.ccy, row.ts); err != nil {
				t.Fatal(err)
			}
		}
	}
	r, err := s.TrueNetProfitSince(ctx, base.Add(-time.Minute*30), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"truth-perp|perp|USDT", "truth-mt5|mt5|USD", "truth-unified|unified|USDT"} {
		hit := false
		for _, c := range r.Covered {
			if c == want {
				hit = true
			}
		}
		if !hit {
			t.Fatalf("%s 应入 Covered（返修前盲面）: %v", want, r.Covered)
		}
	}
}
