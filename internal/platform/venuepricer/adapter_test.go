// adapter_test.go —— D-651 A 段锚：三 venue SpotPrice httptest 桩 + For 无注册
// =ErrNoAdapter 凭证缺口语义（删注册→okx 提案如实拒非裸错验收锚）。
package venuepricer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFor_NoRegistry(t *testing.T) {
	if _, err := For("kraken", Cred{BaseURL: "https://x"}); !errors.Is(err, ErrNoAdapter) {
		t.Fatalf("无注册应 ErrNoAdapter: %v", err)
	}
	if _, err := For("binance", Cred{}); !errors.Is(err, ErrNoAdapter) {
		t.Fatalf("base 缺应同哨兵（凭证缺口）: %v", err)
	}
}

func TestSpotPrice_ThreeVenues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v3/ticker/price": // binance
			fmt.Fprint(w, `{"symbol":"BTCUSDT","price":"81234.5"}`)
		case r.URL.Path == "/api/v5/market/index-tickers": // okx idxPx 锚
			fmt.Fprint(w, `{"code":"0","data":[{"instId":"BTC-USDT","idxPx":"81200.1"}]}`)
		case r.URL.Path == "/api/v4/spot/tickers": // gate
			fmt.Fprint(w, `[{"currency_pair":"BTC_USDT","last":"81180.0"}]`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	for venue, want := range map[string]float64{"binance": 81234.5, "okx": 81200.1, "gate": 81180.0} {
		a, err := For(venue, Cred{BaseURL: srv.URL, Client: srv.Client()})
		if err != nil {
			t.Fatalf("%s: %v", venue, err)
		}
		px, err := a.SpotPrice(ctx, "BTC")
		if err != nil || px != want {
			t.Fatalf("%s SpotPrice=%v,%v want %v", venue, px, err, want)
		}
	}
}

func TestSpotSymbol(t *testing.T) {
	got := map[string]string{}
	for _, v := range []string{"binance", "okx", "gate"} {
		a, _ := For(v, Cred{BaseURL: "https://x"})
		got[v] = a.SpotSymbol("BTC")
	}
	if got["binance"] != "BTCUSDT" || got["okx"] != "BTC-USDT" || got["gate"] != "BTC_USDT" {
		t.Fatalf("符号形错: %v", got)
	}
}
