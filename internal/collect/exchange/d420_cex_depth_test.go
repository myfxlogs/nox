// d420_cex_depth_test.go —— D-420 S2：顶档深度 fetcher 三所形状回归（先红后绿）。
// httptest 复刻各所公开端点形状；取最优档（max bid / min ask，对序不敏感）；
// venue 无此符号（404/error）→ 静默跳过（fail-open，白名单符号在不在所由所定）。
package exchange

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func d420Cfg(bin, gate, okx string) Config {
	return Config{
		Symbols:        []string{},
		BinanceBaseURL: bin,
		GateBaseURL:    gate,
		OKXBaseURL:     okx,
		Client:         &http.Client{},
	}
}

func TestD420_FetchDepth_AllVenues(t *testing.T) {
	bin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"bids":[["100.0","2"],["99.5","1"]],"asks":[["100.2","3"],["100.5","1"]]}`))
	}))
	defer bin.Close()
	gateQuantoCache = sync.Map{} // 测试间隔离（进程级缓存）
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// gate 真实形状（D-112 实拍）：条目 {p:价,s:张数} 对象；张数×乘数折 base
		if strings.Contains(r.URL.Path, "/contracts/") {
			w.Write([]byte(`{"quanto_multiplier":"0.5"}`))
			return
		}
		w.Write([]byte(`{"asks":[{"p":"200.4","s":2},{"p":"200.6","s":4}],"bids":[{"p":"200.0","s":8},{"p":"199.8","s":4}]}`))
	}))
	defer gate.Close()
	okx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// okx 形状：data[0].bids/asks，条目 [px,sz,...]
		w.Write([]byte(`{"code":"0","data":[{"asks":[["300.2","5","0","1"]],"bids":[["300.0","6","0","1"]]}]}`))
	}))
	defer okx.Close()
	cfg := d420Cfg(bin.URL, gate.URL, okx.URL)

	got := map[string]DepthQuote{}
	for _, d := range FetchCexDepths(context.Background(), cfg, []string{"XAU"}) {
		got[d.Venue] = d
	}
	if len(got) != 3 {
		t.Fatalf("want 3 venue quotes, got %v", got)
	}
	if d := got["binance"]; d.Bid1 != 100.0 || d.Ask1 != 100.2 || d.BidQty != 2 || d.AskQty != 3 || d.Symbol != "XAUUSDT" {
		t.Errorf("binance: %+v", d)
	}
	if d := got["gate"]; d.Bid1 != 200.0 || d.Ask1 != 200.4 || d.BidQty != 4 || d.AskQty != 1 || d.Symbol != "XAU_USDT" {
		t.Errorf("gate: %+v", d)
	}
	if d := got["okx"]; d.Bid1 != 300.0 || d.Ask1 != 300.2 || d.BidQty != 6 || d.AskQty != 5 || d.Symbol != "XAU-USDT-SWAP" {
		t.Errorf("okx: %+v", d)
	}
	// MidDepthUSD：顶档中间价×可成交量（min 语义，容量定性）
	if d := got["binance"]; d.MidDepthUSD() < 200 || d.MidDepthUSD() > 201 {
		t.Errorf("binance mid depth want 100.1×2=200.2, got %v", d.MidDepthUSD())
	}
}

func TestD420_FetchDepth_MissingSymbol_Skipped(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fapi/v1/depth" {
			w.Write([]byte(`{"bids":[["1","1"]],"asks":[["1.1","1"]]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ok.Close()
	cfg := d420Cfg(ok.URL, "http://127.0.0.1:1", "http://127.0.0.1:1")
	got := FetchCexDepths(context.Background(), cfg, []string{"SPX500"})
	if len(got) != 1 || got[0].Venue != "binance" {
		t.Errorf("venue 无符号应跳过（fail-open），got %v", got)
	}
}

func TestD420_FetchDepth_GateContractSpecFail_Skipped(t *testing.T) {
	// D-112：contracts 端点失败=乘数不可得 → gate 该 symbol 本轮无深度（fail-closed），
	// 不连累 binance/okx（per-symbol 隔离）。
	bin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"bids":[["100.0","2"]],"asks":[["100.2","3"]]}`))
	}))
	defer bin.Close()
	gateQuantoCache = sync.Map{}
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contracts/") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"asks":[{"p":"200.4","s":2}],"bids":[{"p":"200.0","s":8}]}`))
	}))
	defer gate.Close()
	cfg := d420Cfg(bin.URL, gate.URL, "http://127.0.0.1:1")
	got := map[string]DepthQuote{}
	for _, d := range FetchCexDepths(context.Background(), cfg, []string{"XAU"}) {
		got[d.Venue] = d
	}
	if len(got) != 1 || got["binance"].Venue != "binance" {
		t.Fatalf("gate 乘数失败应只跳过 gate，got %v", got)
	}
}

func TestD420_DepthWhitelist(t *testing.T) {
	// SSOT 白名单：SPX500/XPT/XPD/XAU/CLU（CLU=油族 binance/gate/okx 基名 CL 的 SSOT 记法）
	want := map[string]bool{"SPX500": true, "XPT": true, "XPD": true, "XAU": true, "CL": true}
	if len(DepthWhitelistBases) != len(want) {
		t.Fatalf("whitelist=%v", DepthWhitelistBases)
	}
	for _, b := range DepthWhitelistBases {
		if !want[b] {
			t.Errorf("白名单外基名 %q", b)
		}
	}
}
