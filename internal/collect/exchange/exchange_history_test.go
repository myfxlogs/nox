package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arb/internal/fact"
)

// zeroResolver 模拟首次回填（无历史数据，返回零时间）。
func zeroResolver(context.Context, string, string) (time.Time, error) { return time.Time{}, nil }

// TestBinanceFundingHistory_Poll 解析 Binance fundingRate 历史响应并年化。
// 响应形状：[{fundingTime(ms number), fundingRate(string)}]。
func TestBinanceFundingHistory_Poll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 返回 2 条结算记录，fundingRate=0.0001 → 年化 0.0001×3×365×100=10.95%
		resp := []map[string]any{
			{"fundingTime": float64(1788163200000), "fundingRate": "0.0001"},
			{"fundingTime": float64(1788134400000), "fundingRate": "0.0002"},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := Config{Symbols: []string{"BTC"}, BinanceBaseURL: srv.URL, Client: srv.Client()}
	c := NewBinanceFundingHistory(cfg, zeroResolver)
	out, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 facts, got %d", len(out))
	}
	for _, f := range out {
		if f.Kind != fact.KindFundingHistory {
			t.Errorf("kind want funding_history, got %s", f.Kind)
		}
		if f.Venue != VenueBinance {
			t.Errorf("venue want binance, got %s", f.Venue)
		}
		if f.Unit != fact.UnitPctAnnualized {
			t.Errorf("unit want pct_annualized, got %s", f.Unit)
		}
	}
	// 0.0001 × 3 × 365 × 100 = 10.95
	if out[0].Value < 10.9 || out[0].Value > 11.0 {
		t.Errorf("rate 0.0001 annualized want ~10.95, got %.4f", out[0].Value)
	}
	// 0.0002 × 3 × 365 × 100 = 21.9
	if out[1].Value < 21.8 || out[1].Value > 22.0 {
		t.Errorf("rate 0.0002 annualized want ~21.9, got %.4f", out[1].Value)
	}
	// fundingTime 是毫秒
	if !out[0].Ts.Equal(time.UnixMilli(1788163200000)) {
		t.Errorf("ts want 1788163200000ms, got %v", out[0].Ts)
	}
}

// TestOKXFundingHistory_Poll 解析 OKX funding-rate-history 响应并年化。
// 响应形状：{data:[{fundingRate(string), fundingTime(ms string)}]}。
func TestOKXFundingHistory_Poll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"code": "0",
			"data": []map[string]any{
				{"fundingRate": "0.0001", "fundingTime": "1788163200000"},
				{"fundingRate": "0.00005", "fundingTime": "1788134400000"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := Config{Symbols: []string{"BTC"}, OKXBaseURL: srv.URL, Client: srv.Client()}
	c := NewOKXFundingHistory(cfg, zeroResolver)
	out, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 facts, got %d", len(out))
	}
	if out[0].Venue != VenueOKX {
		t.Errorf("venue want okx, got %s", out[0].Venue)
	}
	// 0.0001 × 3 × 365 × 100 = 10.95
	if out[0].Value < 10.9 || out[0].Value > 11.0 {
		t.Errorf("rate 0.0001 annualized want ~10.95, got %.4f", out[0].Value)
	}
	if !out[0].Ts.Equal(time.UnixMilli(1788163200000)) {
		t.Errorf("ts want 1788163200000ms, got %v", out[0].Ts)
	}
}

// TestGateFundingHistory_Poll 解析 Gate funding_rate 历史响应并年化。
// 响应形状（实测 2026-08-31）：[{"r":"rate(string)","t":sec}]——t 是秒非毫秒。
func TestGateFundingHistory_Poll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := []map[string]any{
			{"r": "0.0001", "t": float64(1788163201)},
			{"r": "0.00003", "t": float64(1788134402)},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cfg := Config{Symbols: []string{"BTC"}, GateBaseURL: srv.URL, Client: srv.Client()}
	c := NewGateFundingHistory(cfg, zeroResolver)
	out, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 facts, got %d", len(out))
	}
	if out[0].Venue != VenueGate {
		t.Errorf("venue want gate, got %s", out[0].Venue)
	}
	// 0.0001 × 3 × 365 × 100 = 10.95
	if out[0].Value < 10.9 || out[0].Value > 11.0 {
		t.Errorf("rate 0.0001 annualized want ~10.95, got %.4f", out[0].Value)
	}
	// Gate t 是秒
	if !out[0].Ts.Equal(time.Unix(1788163201, 0)) {
		t.Errorf("ts want 1788163201s, got %v", out[0].Ts)
	}
}

// TestFundingHistoryFailOpen D-308 S1：单符号失败不连坐全所——cexSymbols 全局表
// 在所间不对称（COPPER 仅 binance、指数仅 gate/okx），缺符号必须静默跳过且
// 正常符号数据仍产出；OKX 分页中途失败的符号整符号弃置（出参只带成功符号）。
func TestFundingHistoryFailOpen(t *testing.T) {
	ctx := context.Background()

	// Binance：COPPERUSDT 400，BTCUSDT 正常 → 只产 BTC facts。
	binSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("symbol") == "COPPERUSDT" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{"fundingTime": float64(1788163200000), "fundingRate": "0.0001"},
		})
	}))
	defer binSrv.Close()
	bin := NewBinanceFundingHistory(
		Config{Symbols: []string{"COPPER", "BTC"}, BinanceBaseURL: binSrv.URL, Client: binSrv.Client()}, zeroResolver)
	out, err := bin.Poll(ctx)
	if err != nil {
		t.Fatalf("binance Poll should not fail on single-symbol error: %v", err)
	}
	if len(out) != 1 || out[0].Symbol != "BTCUSDT" {
		t.Fatalf("binance want only BTCUSDT fact, got %v", out)
	}

	// Gate：COPPER_USDT 400，BTC_USDT 正常。
	gateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("contract") == "COPPER_USDT" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{"r": "0.0001", "t": float64(1788163201)},
		})
	}))
	defer gateSrv.Close()
	gate := NewGateFundingHistory(
		Config{Symbols: []string{"COPPER", "BTC"}, GateBaseURL: gateSrv.URL, Client: gateSrv.Client()}, zeroResolver)
	out, err = gate.Poll(ctx)
	if err != nil {
		t.Fatalf("gate Poll should not fail on single-symbol error: %v", err)
	}
	if len(out) != 1 || out[0].Symbol != "BTCUSDT" {
		t.Fatalf("gate want only BTCUSDT fact, got %v", out)
	}

	// OKX：PGFAIL 首页 100 条（触发翻页）第二页 400 → 整符号弃置零产出；BTC 正常。
	okxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch q.Get("instId") {
		case "COPPER-USDT-SWAP":
			w.WriteHeader(http.StatusBadRequest)
		case "PGFAIL-USDT-SWAP":
			if q.Get("before") != "" { // 第二页失败
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			page := make([]map[string]any, 100)
			for i := range page {
				page[i] = map[string]any{"fundingRate": "0.0001", "fundingTime": "1788163200000"}
			}
			json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": page})
		default:
			json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": []map[string]any{
				{"fundingRate": "0.0001", "fundingTime": "1788163200000"},
			}})
		}
	}))
	defer okxSrv.Close()
	okx := NewOKXFundingHistory(
		Config{Symbols: []string{"COPPER", "PGFAIL", "BTC"}, OKXBaseURL: okxSrv.URL, Client: okxSrv.Client()}, zeroResolver)
	out, err = okx.Poll(ctx)
	if err != nil {
		t.Fatalf("okx Poll should not fail on single-symbol error: %v", err)
	}
	if len(out) != 1 || out[0].Symbol != "BTCUSDT" {
		t.Fatalf("okx want only BTCUSDT fact（PGFAIL 部分页须弃置）, got %v", out)
	}
}

// TestStartTS_FirstBackfill 无历史数据 → now-30d。
func TestStartTS_FirstBackfill(t *testing.T) {
	start, err := startTS(context.Background(), zeroResolver, "binance", "BTCUSDT")
	if err != nil {
		t.Fatalf("startTS: %v", err)
	}
	want := time.Now().Add(-fundingHistoryBackfill)
	diff := start.Sub(want)
	if diff > time.Second || diff < -time.Second {
		t.Errorf("first backfill want ~%v, got %v (diff %v)", want, start, diff)
	}
}

// TestStartTS_Incremental 有历史数据 → last+1s（增量补采，避免重复）。
func TestStartTS_Incremental(t *testing.T) {
	last := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	resolver := func(context.Context, string, string) (time.Time, error) { return last, nil }
	start, err := startTS(context.Background(), resolver, "binance", "BTCUSDT")
	if err != nil {
		t.Fatalf("startTS: %v", err)
	}
	want := last.Add(time.Second)
	if !start.Equal(want) {
		t.Errorf("incremental want %v, got %v", want, start)
	}
}
