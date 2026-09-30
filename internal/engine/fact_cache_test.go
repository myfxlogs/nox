// fact_cache_test.go —— D-060 F4：FactCache trade_mode 消费（venueKey 键口径）+ TTL 过期视为未知。
package engine

import (
	"testing"
	"time"

	"arb/internal/fact"
)

func TestFactCacheTradeModes(t *testing.T) {
	fc := NewFactCache()
	now := time.Now()
	fc.UpdateFacts([]fact.Fact{
		{Kind: fact.KindTradeMode, Venue: "52993526", Symbol: "EURUSD", Value: 4, Unit: fact.UnitMode, Ts: now, Src: "mt5"},
		{Kind: fact.KindTradeMode, Venue: "pxbt", Symbol: "BTCUSDT", Value: 0, Unit: fact.UnitMode, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapLong, Venue: "52993526", Symbol: "EURUSD", Value: 1, Ts: now, Src: "mt5"},
	})
	// trade_mode 键 = venueKey（Venue+":"+Src），与 swap 的 venue 名键并存、互不对齐。
	modes := fc.TradeModes()
	if got := modes["EURUSD"]["52993526:mt5"]; got != fact.TradeModeFull {
		t.Errorf("TradeModes[EURUSD][52993526:mt5] = %d, want Full(4)", got)
	}
	if got := modes["BTCUSDT"]["pxbt:mt5"]; got != fact.TradeModeDisabled {
		t.Errorf("TradeModes[BTCUSDT][pxbt:mt5] = %d, want Disabled(0)", got)
	}
	if sw := fc.SwapFacts(); sw["EURUSD"]["52993526"][0] != 1 {
		t.Errorf("swap 名键被污染: %+v", sw)
	}
}

func TestFactCacheTradeModeTTLExpiry(t *testing.T) {
	fc := NewFactCache()
	fc.UpdateFacts([]fact.Fact{
		{Kind: fact.KindTradeMode, Venue: "v", Symbol: "X", Value: 4, Unit: fact.UnitMode, Ts: time.Now().Add(-25 * time.Hour), Src: "mt5"},
	})
	// 缺省 TTL 24h → 25h 前的 Fact 过期，视为未知（放行）。
	if m := fc.TradeModes(); len(m) != 0 {
		t.Errorf("过期 trade_mode 仍可见: %+v", m)
	}
	// TTL 注入生效：放宽到 48h → 可见。
	fc.SetTradeModeTTL(48 * time.Hour)
	if got := fc.TradeModes()["X"]["v:mt5"]; got != fact.TradeModeFull {
		t.Errorf("TTL 48h 下应可见, got %d", got)
	}
}
