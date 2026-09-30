// d591_radar_test.go D-591 S6：radar binance 源读库化——exchangeInfo 日缓存
// （新鲜直用/拉败回落过期缓存）+ facts/ticks 批查映射（覆盖符号换算对、未覆盖诚实零值）。
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// d591CacheFile 写 exchangeInfo 缓存文件（fetchedAt 偏移可负=过期）。
func d591CacheFile(t *testing.T, dir string, age time.Duration, syms map[string]string) {
	t.Helper()
	payload := binanceExchangeInfo{FetchedAt: time.Now().Add(-age)}
	for sym, underlying := range syms {
		payload.Symbols = append(payload.Symbols, struct {
			Symbol         string  `json:"symbol"`
			BaseAsset      string  `json:"baseAsset"`
			QuoteAsset     string  `json:"quoteAsset"`
			ContractType   string  `json:"contractType"`
			UnderlyingType string  `json:"underlyingType"`
			Status         string  `json:"status"`
			OnboardDate    flexInt `json:"onboardDate"`
		}{sym, sym[:3], "USDT", "PERPETUAL", underlying, "TRADING", flexInt(1720000000000)})
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "binance-exchangeinfo.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestD591_RadarBinanceReads：库内种子（ticks/funding/interval/vol24h）→ Scan 行
// 换算对（FundingRate=年化×ivlSec 换算、IntervalH=4、Vol24h 直取、Mark=tick mid）；
// 未覆盖第二符号诚实零值；exchangeInfo 走新鲜缓存零 REST。
func TestD591_RadarBinanceReads(t *testing.T) {
	ctx := context.Background()
	s, err := pgstore.New(ctx, testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool := s.Pool()
	for _, q := range []string{
		`INSERT INTO ticks (ts, venue, venue_type, symbol, bid, ask) VALUES (now(), 'binance', 'perp', 'D591BTUSDT', 4360, 4362)`,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('funding', 'binance', 'D591BTUSDT', 10.95, 'pct_annualized', now(), 'binance_ws_markprice')`,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('funding_interval', 'binance', 'D591BTUSDT', 14400, 'seconds', now(), 'binance_ws_markprice')`,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('vol24h', 'binance', 'D591BTUSDT', 6e6, 'usd', now(), 'binance_ws_ticker')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM ticks WHERE symbol='D591BTUSDT'`)
		pool.Exec(ctx, `DELETE FROM facts WHERE symbol='D591BTUSDT'`)
	})

	dir := t.TempDir()
	d591CacheFile(t, dir, 0, map[string]string{"D591BTUSDT": "COIN", "D591NUUSDT": "COIN"})
	b := binance{hc: http.DefaultClient, base: "http://127.0.0.1:1", pool: pool, cacheDir: dir}
	rows, err := b.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bySym := map[string]Row{}
	for _, r := range rows {
		bySym[r.Symbol] = r
	}
	covered, ok := bySym["D591BTUSDT"]
	if !ok {
		t.Fatal("缓存目录符号应在行集")
	}
	wantFR := 10.95 * 14400 / (365 * 100 * 86400)
	if diff := covered.FundingRate - wantFR; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("FundingRate want %v, got %v", wantFR, covered.FundingRate)
	}
	if covered.IntervalH != 4 || covered.Vol24h != 6e6 || covered.Mark != 4361 {
		t.Errorf("IntervalH/Mark want 4/4361, got %d/%v; Vol24h=%v", covered.IntervalH, covered.Mark, covered.Vol24h)
	}
	uncovered := bySym["D591NUUSDT"]
	if uncovered.FundingRate != 0 || uncovered.Vol24h != 0 || uncovered.Mark != 0 {
		t.Errorf("未覆盖符号应诚实零值: %+v", uncovered)
	}
}

// TestD591_RadarExchangeInfoCacheFallback：缓存过期 + 拉取失败 → 回落过期缓存不炸。
func TestD591_RadarExchangeInfoCacheFallback(t *testing.T) {
	dir := t.TempDir()
	d591CacheFile(t, dir, 25*time.Hour, map[string]string{"D591BTUSDT": "COIN"})
	b := binance{hc: http.DefaultClient, base: "http://127.0.0.1:1", cacheDir: dir}
	ei, err := b.exchangeInfoCached(context.Background())
	if err != nil {
		t.Fatalf("过期缓存应回落: %v", err)
	}
	if len(ei.Symbols) != 1 || ei.Symbols[0].Symbol != "D591BTUSDT" {
		t.Fatalf("回落缓存内容不对: %+v", ei.Symbols)
	}
}
