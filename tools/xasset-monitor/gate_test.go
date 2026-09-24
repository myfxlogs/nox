// gate_test.go D-462：fetchGateTicker 四例 + pair 键与消费端口径锚定（T1/T2）。
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// gateStub 按路径回放 gate 公开端点响应。
func gateStub(t *testing.T, tickerBody, contractBody string, tickerStatus, contractStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v4/futures/usdt/tickers"):
			w.WriteHeader(tickerStatus)
			w.Write([]byte(tickerBody))
		case strings.HasPrefix(r.URL.Path, "/api/v4/futures/usdt/contracts/"):
			w.WriteHeader(contractStatus)
			w.Write([]byte(contractBody))
		default:
			w.WriteHeader(404)
		}
	}))
}

func withGateBase(t *testing.T, srv *httptest.Server) {
	t.Helper()
	orig := gateAPIBase
	gateAPIBase = srv.URL
	t.Cleanup(func() { gateAPIBase = orig })
}

// T1-1：正解——bid/ask/funding/periods（interval 14400s→6 期/日，9-24 实测值）。
func TestFetchGateTicker_OK(t *testing.T) {
	srv := gateStub(t,
		`[{"highest_bid":"4283.31","lowest_ask":"4283.32","contract":"XAU_USDT"}]`,
		`{"funding_rate":"0.000114","funding_interval":14400}`, 200, 200)
	withGateBase(t, srv)
	bid, ask, fr, ppd, err := fetchGateTicker(context.Background(), srv.Client(), "XAU_USDT")
	if err != nil {
		t.Fatal(err)
	}
	if bid != 4283.31 || ask != 4283.32 || fr != 0.000114 || ppd != 6 {
		t.Fatalf("bid=%v ask=%v fr=%v ppd=%d", bid, ask, fr, ppd)
	}
}

// T1-2：ticker 空数组→err。
func TestFetchGateTicker_Empty(t *testing.T) {
	srv := gateStub(t, `[]`, `{}`, 200, 200)
	withGateBase(t, srv)
	if _, _, _, _, err := fetchGateTicker(context.Background(), srv.Client(), "XAU_USDT"); err == nil {
		t.Fatal("empty ticker should err")
	}
}

// T1-3：非 200→err。
func TestFetchGateTicker_Status(t *testing.T) {
	srv := gateStub(t, `not-json`, ``, 500, 200)
	withGateBase(t, srv)
	if _, _, _, _, err := fetchGateTicker(context.Background(), srv.Client(), "XAU_USDT"); err == nil {
		t.Fatal("non-200 should err")
	}
}

// T1-4：funding_interval≤0→err（禁静默回退 D-079）。
func TestFetchGateTicker_ZeroInterval(t *testing.T) {
	srv := gateStub(t,
		`[{"highest_bid":"1","lowest_ask":"2"}]`,
		`{"funding_rate":"0.0001","funding_interval":0}`, 200, 200)
	withGateBase(t, srv)
	if _, _, _, _, err := fetchGateTicker(context.Background(), srv.Client(), "XAU_USDT"); err == nil {
		t.Fatal("interval=0 should err")
	}
}

// T2：gate pair 键与消费端口径钉死——engine_opp.go pairKey=CanonicalSell+"-"+TrimSuffix(Canonical,"USD")。
// canonical=MT 侧品种（XAUUSD/XPTUSD/XPDUSD），CanonicalSell=CEX 侧 canonical（XAUUSDT 等去 _）。
func TestGatePairKeys(t *testing.T) {
	want := map[string][2]string{ // sym → {canonicalSell, canonical} → pair
		"XAU_USDT": {"XAUUSDT", "XAUUSD"},
		"XPT_USDT": {"XPTUSDT", "XPTUSD"},
		"XPD_USDT": {"XPDUSDT", "XPDUSD"},
	}
	seen := map[string]string{}
	for _, p := range monitorPairs() {
		if p.cexVenue == "gate:perp" {
			seen[p.sym] = p.pair
		}
	}
	if len(seen) != 3 {
		t.Fatalf("gate pairs=%v", seen)
	}
	for sym, cs := range want {
		exp := cs[0] + "-" + strings.TrimSuffix(cs[1], "USD")
		if seen[sym] != exp {
			t.Errorf("pair[%s]=%s want %s", sym, seen[sym], exp)
		}
	}
}
