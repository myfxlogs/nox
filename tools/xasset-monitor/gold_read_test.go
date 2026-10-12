// gold_read_test.go D-591 S2/S3/S5：三所 Gold 读库三态（新鲜→值同义换算对 /
// 陈旧→报错不猜 / 缺行→报错）+ 符号键形转换（okx instId/gate contract→canonical）。
// 守门定标依据：6h 实测 ticks 行间隔 binance p100=118s / gate p99=74s / okx p99=61s。
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	d591Sym     = "D591TUSDT"
	d591Annual  = 10.95
	d591IvlSec  = 14400.0 // 4h
	d591PPD     = 6
	d591RateErr = 1e-12
)

// d591Seed 按 age 播 ticks/funding/funding_interval 三行（负 age=不播）。
func d591Seed(t *testing.T, pool *pgxpool.Pool, venue, src string, tickAge, fundAge, ivlAge time.Duration) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if tickAge >= 0 {
		if _, err := pool.Exec(ctx, `INSERT INTO ticks (ts, venue, venue_type, symbol, bid, ask)
			VALUES ($1, $2, 'perp', $3, 4360.5, 4361.5)`, now.Add(-tickAge), venue, d591Sym); err != nil {
			t.Fatal(err)
		}
	}
	if fundAge >= 0 {
		if _, err := pool.Exec(ctx, `INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
			VALUES ('funding', $1, $2, $3, 'pct_annualized', $4, $5)`, venue, d591Sym, d591Annual, now.Add(-fundAge), src); err != nil {
			t.Fatal(err)
		}
	}
	if ivlAge >= 0 {
		if _, err := pool.Exec(ctx, `INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
			VALUES ('funding_interval', $1, $2, $3, 'seconds', $4, 'd591_test')`, venue, d591Sym, d591IvlSec, now.Add(-ivlAge)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM ticks WHERE venue=$1 AND symbol=$2`, venue, d591Sym)
		pool.Exec(ctx, `DELETE FROM facts WHERE venue=$1 AND symbol=$2`, venue, d591Sym)
	})
}

// d591Fetch 按 venue 调对应 Gold fetcher（原生符号入参，覆盖键形转换）。
func d591Fetch(t *testing.T, pool *pgxpool.Pool, venue string) (bid, ask, fr float64, ppd int, err error) {
	t.Helper()
	switch venue {
	case "binance":
		return fetchBinanceGold(context.Background(), pool, d591Sym)
	case "okx":
		return fetchOkxGold(context.Background(), pool, "D591T-USDT-SWAP")
	case "gate":
		return fetchGateGold(context.Background(), pool, "D591T_USDT")
	}
	t.Fatalf("unknown venue %s", venue)
	return 0, 0, 0, 0, nil
}

// 三所 × 六态表驱动：新鲜换算对 / tick 陈旧 / funding 缺·陈旧 / interval 缺·陈旧。
func TestD591_GoldReadThreeStates(t *testing.T) {
	cases := []struct {
		venue, src               string
		tickAge, fundAge, ivlAge time.Duration // -1=不播
		wantErr                  string        // 空=期望成功
	}{
		{"binance", "binance_ws_markprice", 10 * time.Second, time.Minute, time.Minute, ""},
		{"okx", "okx_ws_v5", 10 * time.Second, time.Minute, time.Minute, ""},
		{"gate", "gate_ws_futures", 10 * time.Second, time.Minute, time.Minute, ""},
		{"binance", "binance_ws_markprice", 130 * time.Second, time.Minute, time.Minute, "stale"},
		{"okx", "okx_ws_v5", 130 * time.Second, time.Minute, time.Minute, "stale"},
		{"gate", "gate_ws_futures", 130 * time.Second, time.Minute, time.Minute, "stale"},
		{"binance", "binance_ws_markprice", 10 * time.Second, -1, time.Minute, "facts funding"},
		{"okx", "okx_ws_v5", 10 * time.Second, -1, time.Minute, "facts funding"},
		{"gate", "gate_ws_futures", 10 * time.Second, -1, time.Minute, "facts funding"},
		{"binance", "binance_ws_markprice", 10 * time.Second, 16 * time.Minute, time.Minute, "stale"},
		{"okx", "okx_ws_v5", 10 * time.Second, 16 * time.Minute, time.Minute, "stale"},
		{"gate", "gate_ws_futures", 10 * time.Second, 16 * time.Minute, time.Minute, "stale"},
		{"binance", "binance_ws_markprice", 10 * time.Second, time.Minute, -1, "funding_interval"},
		{"okx", "okx_ws_v5", 10 * time.Second, time.Minute, -1, "funding_interval"},
		{"gate", "gate_ws_futures", 10 * time.Second, time.Minute, -1, "funding_interval"},
		// interval 守门定标写源节奏 25h——20h 放行（日轮 gate 常态）/26h 拒。
		{"binance", "binance_ws_markprice", 10 * time.Second, time.Minute, 20 * time.Hour, ""},
		{"okx", "okx_ws_v5", 10 * time.Second, time.Minute, 20 * time.Hour, ""},
		{"gate", "gate_ws_futures", 10 * time.Second, time.Minute, 20 * time.Hour, ""},
		{"binance", "binance_ws_markprice", 10 * time.Second, time.Minute, 26 * time.Hour, "stale"},
		{"okx", "okx_ws_v5", 10 * time.Second, time.Minute, 26 * time.Hour, "stale"},
		{"gate", "gate_ws_futures", 10 * time.Second, time.Minute, 26 * time.Hour, "stale"},
	}
	for _, tc := range cases {
		t.Run(tc.venue, func(t *testing.T) {
			s := d335r1Store(t)
			d591Seed(t, s.Pool(), tc.venue, tc.src, tc.tickAge, tc.fundAge, tc.ivlAge)
			bid, ask, fr, ppd, err := d591Fetch(t, s.Pool(), tc.venue)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want err含%q, got %v (bid=%v)", tc.wantErr, err, bid)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if bid != 4360.5 || ask != 4361.5 {
				t.Errorf("bid/ask want 4360.5/4361.5, got %v/%v", bid, ask)
			}
			if want := d591Annual * d591IvlSec / (365 * 100 * 86400); fr-want > d591RateErr || want-fr > d591RateErr {
				t.Errorf("fundingRate want %v, got %v", want, fr)
			}
			if ppd != d591PPD {
				t.Errorf("periodsPerDay want %d, got %d", d591PPD, ppd)
			}
		})
	}
}
