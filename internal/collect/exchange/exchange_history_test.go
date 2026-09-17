package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
// D-321 S2：mock 补 funding-rate 周期探测端点（8h 差值 → ×3，值断言 10.95 不变）。
func TestOKXFundingHistory_Poll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/public/funding-rate" {
			json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": []map[string]any{
				{"fundingRate": "0.0001", "fundingTime": "1788163200000", "nextFundingTime": "1788192000000"},
			}})
			return
		}
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
			if q.Get("after") != "" { // D-321 S1 修后翻页游标=after：第二页失败
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

// —— D-321 S3：OKX 增量方向/翻页/空窗/周期锚 ——

// okxPageReq 记录一次 funding-rate-history 请求的方向参数（T1/T2 断言用）。
type okxPageReq struct{ before, after string }

// okxRealSemanticsHandler 按真实 OKX 语义 mock funding-rate-history（D-321 API 实证：
// before=X 只回「更新于 X」、after=X 只回「早于 X」，降序返回，limit 截断）；
// 周期探测端点回 8h 差值（与方向断言无关）。
func okxRealSemanticsHandler(recs []map[string]any, reqs *[]okxPageReq) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/public/funding-rate" {
			json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": []map[string]any{
				{"fundingTime": "1788998400000", "nextFundingTime": "1789027200000"},
			}})
			return
		}
		q := r.URL.Query()
		*reqs = append(*reqs, okxPageReq{before: q.Get("before"), after: q.Get("after")})
		var beforeMs, afterMs int64
		if s := q.Get("before"); s != "" {
			beforeMs, _ = strconv.ParseInt(s, 10, 64)
		}
		if s := q.Get("after"); s != "" {
			afterMs, _ = strconv.ParseInt(s, 10, 64)
		}
		limit := 100
		if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
			limit = n
		}
		out := make([]map[string]any, 0, limit)
		for _, rec := range recs { // recs 须降序
			ftMs, _ := strconv.ParseInt(rec["fundingTime"].(string), 10, 64)
			if q.Get("before") != "" && ftMs <= beforeMs {
				continue
			}
			if q.Get("after") != "" && ftMs >= afterMs {
				continue
			}
			if len(out) == limit {
				break
			}
			out = append(out, rec)
		}
		json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": out})
	}
}

// okxRecs 构造降序罐装记录（fundingTime 毫秒字符串）。
func okxRecs(ftMs ...int64) []map[string]any {
	recs := make([]map[string]any, len(ftMs))
	for i, ms := range ftMs {
		recs[i] = map[string]any{"fundingRate": "0.0001", "fundingTime": strconv.FormatInt(ms, 10)}
	}
	return recs
}

// TestOKXFundingHistory_IncrementalDirection D-321 T1 主锚：增量必须取「比 watermark 更新」。
// mock 实现真实 OKX 语义，罐装数据含 watermark 两侧记录；旧码把 watermark 塞 after
// （取更早）→ 只回旧集 → RED。
func TestOKXFundingHistory_IncrementalDirection(t *testing.T) {
	t0 := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC) // watermark=最后已知结算
	var reqs []okxPageReq
	// 降序罐装：3 条新于 t0（09-10 08:00/16:00、09-11 00:00）+ 2 条旧于 t0（09-09 16:00/08:00）
	recs := okxRecs(1789084800000, 1789056000000, 1789027200000, 1788969600000, 1788940800000)
	srv := httptest.NewServer(okxRealSemanticsHandler(recs, &reqs))
	defer srv.Close()

	resolver := func(context.Context, string, string) (time.Time, error) { return t0, nil }
	cfg := Config{Symbols: []string{"BTC"}, OKXBaseURL: srv.URL, Client: srv.Client()}
	out, err := NewOKXFundingHistory(cfg, resolver).Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	want := map[int64]bool{1789027200000: true, 1789056000000: true, 1789084800000: true}
	if len(out) != len(want) {
		t.Fatalf("want only %d records newer than watermark, got %d", len(want), len(out))
	}
	for _, f := range out {
		if !want[f.Ts.UnixMilli()] {
			t.Errorf("produced record not newer than watermark: ts=%d", f.Ts.UnixMilli())
		}
	}
}

// TestOKXFundingHistory_PagingWithinWindow D-321 T2：watermark 之后 >100 条须窗口内翻页——
// 第二请求带 after=页1最旧，共产出 150 条全部新于 watermark 且无重。
func TestOKXFundingHistory_PagingWithinWindow(t *testing.T) {
	t0 := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	t0ms := t0.UnixMilli()
	const step = int64(8 * 3600 * 1000)
	recs := make([]map[string]any, 150) // 降序：i=0 最新
	for i := range recs {
		ft := t0ms + int64(150-i)*step
		recs[i] = map[string]any{"fundingRate": "0.0001", "fundingTime": strconv.FormatInt(ft, 10)}
	}
	var reqs []okxPageReq
	srv := httptest.NewServer(okxRealSemanticsHandler(recs, &reqs))
	defer srv.Close()

	resolver := func(context.Context, string, string) (time.Time, error) { return t0, nil }
	cfg := Config{Symbols: []string{"BTC"}, OKXBaseURL: srv.URL, Client: srv.Client()}
	out, err := NewOKXFundingHistory(cfg, resolver).Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(reqs) != 2 {
		t.Fatalf("want 2 paged requests, got %d: %+v", len(reqs), reqs)
	}
	if reqs[0].before == "" || reqs[0].after != "" {
		t.Errorf("page1 want before=<watermark> without after, got %+v", reqs[0])
	}
	if want := recs[99]["fundingTime"]; reqs[1].after != want { // 页1最旧=第 100 新
		t.Errorf("page2 want after=<page1 oldest %s>, got %+v", want, reqs[1])
	}
	if len(out) != 150 {
		t.Fatalf("want 150 facts, got %d", len(out))
	}
	seen := map[int64]bool{}
	for _, f := range out {
		ms := f.Ts.UnixMilli()
		if ms <= t0ms {
			t.Errorf("record not newer than watermark: %d", ms)
		}
		if seen[ms] {
			t.Errorf("duplicate record: %d", ms)
		}
		seen[ms] = true
	}
}

// TestOKXFundingHistory_EmptyWindow D-321 T3：watermark 新于全部记录 → 0 facts 无错。
// 旧码把 watermark 塞 after（取更早）→ 反而吐出全部旧记录 → RED。
func TestOKXFundingHistory_EmptyWindow(t *testing.T) {
	var reqs []okxPageReq
	recs := okxRecs(1789084800000, 1789056000000, 1789027200000) // 全部旧于 watermark
	srv := httptest.NewServer(okxRealSemanticsHandler(recs, &reqs))
	defer srv.Close()

	t0 := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	resolver := func(context.Context, string, string) (time.Time, error) { return t0, nil }
	cfg := Config{Symbols: []string{"BTC"}, OKXBaseURL: srv.URL, Client: srv.Client()}
	out, err := NewOKXFundingHistory(cfg, resolver).Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("watermark newer than all records: want 0 facts, got %d", len(out))
	}
}

// TestOKXFundingHistory_PeriodsProbe D-321 T4：funding-rate 端点 ft/nft 差值探测结算周期
// （4h 差值 → ×6），端点 400 → 回退 ×3。
func TestOKXFundingHistory_PeriodsProbe(t *testing.T) {
	t0 := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	resolver := func(context.Context, string, string) (time.Time, error) { return t0, nil }
	hist := []map[string]any{ // 降序，均新于 watermark
		{"fundingRate": "0.0001", "fundingTime": "1789056000000"},
		{"fundingRate": "0.0001", "fundingTime": "1789027200000"},
	}

	t.Run("probe4h", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v5/public/funding-rate" { // CL-USDT-SWAP 实测形态：4h 结算
				json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": []map[string]any{
					{"fundingTime": "1789027200000", "nextFundingTime": "1789041600000"},
				}})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": hist})
		}))
		defer srv.Close()
		cfg := Config{Symbols: []string{"BTC"}, OKXBaseURL: srv.URL, Client: srv.Client()}
		out, err := NewOKXFundingHistory(cfg, resolver).Poll(context.Background())
		if err != nil {
			t.Fatalf("Poll: %v", err)
		}
		if len(out) != 2 {
			t.Fatalf("want 2 facts, got %d", len(out))
		}
		// 0.0001 × 6 × 365 × 100 = 21.9
		for _, f := range out {
			if f.Value < 21.8 || f.Value > 22.0 {
				t.Errorf("4h probe want ~21.9, got %.4f", f.Value)
			}
		}
	})

	t.Run("probeFail", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v5/public/funding-rate" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": hist})
		}))
		defer srv.Close()
		cfg := Config{Symbols: []string{"BTC"}, OKXBaseURL: srv.URL, Client: srv.Client()}
		out, err := NewOKXFundingHistory(cfg, resolver).Poll(context.Background())
		if err != nil {
			t.Fatalf("Poll: %v", err)
		}
		if len(out) != 2 {
			t.Fatalf("want 2 facts, got %d", len(out))
		}
		// 探测失败回退 ×3：0.0001 × 3 × 365 × 100 = 10.95
		for _, f := range out {
			if f.Value < 10.9 || f.Value > 11.0 {
				t.Errorf("probe-fail fallback want ~10.95, got %.4f", f.Value)
			}
		}
	})
}
