package dashboard

import (
	"testing"

	"nox/internal/store/pgstore"
)

// TestStreamableBinanceSpot D-459：binance:spot real 行起 ws-api 流；spot demo 行不起（
// testnet ws-api 端点不在代理路由，保持 REST 轮询兜底）。
func TestStreamableBinanceSpot(t *testing.T) {
	cases := []struct {
		c    pgstore.ExchangeConfig
		want bool
	}{
		{pgstore.ExchangeConfig{Venue: "binance", VenueType: "spot", BaseURL: "https://p", Simulated: false}, true},
		{pgstore.ExchangeConfig{Venue: "binance", VenueType: "spot", BaseURL: "https://p", Simulated: true}, false},
		{pgstore.ExchangeConfig{Venue: "binance", VenueType: "spot", BaseURL: "", Simulated: false}, false},
		{pgstore.ExchangeConfig{Venue: "binance", VenueType: "perp", BaseURL: "https://p", Simulated: true}, true},
	}
	for i, tc := range cases {
		if got := streamable(tc.c); got != tc.want {
			t.Fatalf("case %d streamable=%v want %v", i, got, tc.want)
		}
	}
}
