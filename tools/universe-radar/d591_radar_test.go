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
	testutil.EnsureTicksPartition(t, s)
	pool := s.Pool()
	for _, q := range []string{
		`INSERT INTO ticks (ts, venue, venue_type, symbol, bid, ask) VALUES (now(), 'binance', 'perp', 'D591BTUSDT', 4360, 4362)`,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('funding', 'binance', 'D591BTUSDT', 10.95, 'pct_annualized', now(), 'binance_ws_markprice')`,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('funding_interval', 'binance', 'D591BTUSDT', 14400, 'seconds', now(), 'binance_ws_markprice')`,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('vol24h', 'binance', 'D591BTUSDT', 6e6, 'usd', now(), 'binance_ws_ticker')`,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('index_price', 'binance', 'D591BTUSDT', 4360.8, 'usd', now(), 'binance_ws_markprice')`,
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
	// R2：index_price 库源 → Row.Index 填值（LastBasisBps 不再 NULL 化）；未覆盖诚实 0。
	if covered.Index != 4360.8 {
		t.Errorf("Index want 4360.8（index_price 同帧落库）, got %v", covered.Index)
	}
	uncovered := bySym["D591NUUSDT"]
	if uncovered.FundingRate != 0 || uncovered.Vol24h != 0 || uncovered.Mark != 0 || uncovered.Index != 0 {
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

// TestD591R3_RadarGateReadsDB：gate 源纯读库——ticks 24h 活跃符号=宇宙，funding
// 换算对（年化×ivlSec），interval 日轮 25h 窗，未覆盖字段诚实零值。
func TestD591R3_RadarGateReadsDB(t *testing.T) {
	ctx := context.Background()
	s, err := pgstore.New(ctx, testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	testutil.EnsureTicksPartition(t, s)
	pool := s.Pool()
	for _, q := range []string{
		`INSERT INTO ticks (ts, venue, venue_type, symbol, bid, ask) VALUES (now(), 'gate', 'perp', 'D591GTUSDT', 4360, 4360)`,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('funding', 'gate', 'D591GTUSDT', 10.95, 'pct_annualized', now(), 'gate_ws_futures')`,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('funding_interval', 'gate', 'D591GTUSDT', 14400, 'seconds', now() - interval '20 hours', 'gate_v4_contract_meta')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM ticks WHERE symbol='D591GTUSDT'`)
		pool.Exec(ctx, `DELETE FROM facts WHERE symbol='D591GTUSDT'`)
	})
	rows, err := (gate{pool: pool}).Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var r *Row
	for i := range rows {
		if rows[i].Symbol == "D591GTUSDT" {
			r = &rows[i]
		}
	}
	if r == nil {
		t.Fatal("gate 库内符号未入宇宙")
	}
	wantFR := 10.95 * 14400 / (365 * 100 * 86400)
	if diff := r.FundingRate - wantFR; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("FundingRate want %v, got %v", wantFR, r.FundingRate)
	}
	if r.IntervalH != 4 || r.Mark != 4360 || r.Status != "TRADING" || r.Base != "D591GT" {
		t.Errorf("row want IntervalH=4 Mark=4360 TRADING Base=D591GT, got %+v", r)
	}
	if r.CtVal != nil || r.Index != 0 || r.Vol24h != 0 || !r.LaunchTS.IsZero() {
		t.Errorf("无库源字段应诚实零值: %+v", r)
	}
}

// TestD591R3_RadarOkxCacheAndDB：okx instruments/tickers 走 24h 缓存（零 REST）、
// funding 读 facts；未覆盖符号 funding 零值。
func TestD591R3_RadarOkxCacheAndDB(t *testing.T) {
	ctx := context.Background()
	s, err := pgstore.New(ctx, testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	testutil.EnsureTicksPartition(t, s)
	pool := s.Pool()
	for _, q := range []string{
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('funding', 'okx', 'D591OCUSDT', 10.95, 'pct_annualized', now(), 'okx_ws_v5')`,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ('funding_interval', 'okx', 'D591OCUSDT', 14400, 'seconds', now(), 'okx_ws_v5')`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM facts WHERE symbol='D591OCUSDT'`) })

	dir := t.TempDir()
	snap := okxSwapCache{FetchedAt: time.Now(),
		Instruments: []okxInstrument{
			{InstID: "D591OC-USDT-SWAP", State: "live", ListTime: 1720000000000, CtVal: 0.01},
			{InstID: "D591OU-USDT-SWAP", State: "live", ListTime: 1720000000000, CtVal: 0.01},
			{InstID: "BTC-USDT", State: "live"}, // 非 SWAP 滤除
		},
		Tickers: []okxTicker{
			{InstID: "D591OC-USDT-SWAP", VolCcy24h: 1000, Last: 4361},
		}}
	raw, _ := json.Marshal(snap)
	if err := os.WriteFile(filepath.Join(dir, "okx-swap.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// base 指向死端口——缓存命中则零 REST。
	rows, err := (okx{hc: http.DefaultClient, base: "http://127.0.0.1:1", pool: pool, cacheDir: dir}).Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bySym := map[string]Row{}
	for _, r := range rows {
		bySym[r.Symbol] = r
	}
	cov, ok := bySym["D591OC-USDT-SWAP"]
	if !ok {
		t.Fatal("缓存 instruments 符号应在行集")
	}
	wantFR := 10.95 * 14400 / (365 * 100 * 86400)
	if diff := cov.FundingRate - wantFR; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("FundingRate want %v, got %v", wantFR, cov.FundingRate)
	}
	if cov.IntervalH != 4 || cov.Mark != 4361 || cov.Vol24h != 4361000 || cov.Status != "TRADING" {
		t.Errorf("row 不对: %+v", cov)
	}
	if cov.CtVal == nil || *cov.CtVal != 0.01 {
		t.Errorf("CtVal want 0.01, got %v", cov.CtVal)
	}
	unc := bySym["D591OU-USDT-SWAP"]
	if unc.FundingRate != 0 || unc.IntervalH != 0 {
		t.Errorf("未覆盖符号 funding 应零值: %+v", unc)
	}
}

// TestD591R3_OkxCacheStaleFallback：缓存过期+拉败→回落过期缓存（binance 同款形制）。
func TestD591R3_OkxCacheStaleFallback(t *testing.T) {
	dir := t.TempDir()
	snap := okxSwapCache{FetchedAt: time.Now().Add(-25 * time.Hour),
		Instruments: []okxInstrument{{InstID: "D591OC-USDT-SWAP", State: "live", CtVal: 0.01}}}
	raw, _ := json.Marshal(snap)
	if err := os.WriteFile(filepath.Join(dir, "okx-swap.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := (okx{hc: http.DefaultClient, base: "http://127.0.0.1:1", cacheDir: dir}).swapCached(context.Background())
	if err != nil {
		t.Fatalf("过期缓存应回落: %v", err)
	}
	if len(out.Instruments) != 1 || out.Instruments[0].InstID != "D591OC-USDT-SWAP" {
		t.Fatalf("回落缓存内容不对: %+v", out.Instruments)
	}
}
