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
	if mode, ok := fc.TradeMode("BTCUSDT", "pxbt:mt5"); !ok || mode != fact.TradeModeDisabled {
		t.Errorf("TradeMode(BTCUSDT, pxbt:mt5) = (%d, %v), want (0, true)", mode, ok)
	}
	if sw := fc.SwapFacts(); sw["EURUSD"]["52993526"][0] != 1 {
		t.Errorf("swap 名键被污染: %+v", sw)
	}
	// 未知 canonical / venueKey → ok=false。
	if _, ok := fc.TradeMode("EURUSD", "nope:mt5"); ok {
		t.Error("未知 venueKey 应 ok=false")
	}
}

// TestFactCacheSwapFreeVenues D-300 裁 9：白名单 venue 的 swap facts 消费侧置 0（不读出）；
// 非白名单 venue 原样读出（facts 名义值污染不影响白名单外消费）。
func TestFactCacheSwapFreeVenues(t *testing.T) {
	fc := NewFactCache()
	fc.UpdateFacts([]fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "3001157", Symbol: "XAUUSD", Value: -1.2481, Ts: time.Now(), Src: "mt5"},
		{Kind: fact.KindSwapLong, Venue: "52993526", Symbol: "EURUSD", Value: -0.5, Ts: time.Now(), Src: "mt5"},
	})
	// 未设白名单 → 全读出
	if sw := fc.SwapFacts(); sw["XAUUSD"]["3001157"][0] != -1.2481 {
		t.Fatalf("no whitelist: want -1.2481, got %+v", sw)
	}
	fc.SetSwapFreeVenues([]string{"3001157"})
	sw := fc.SwapFacts()
	if _, ok := sw["XAUUSD"]["3001157"]; ok {
		t.Fatalf("swap-free venue must be hidden, got %+v", sw["XAUUSD"])
	}
	if sw["EURUSD"]["52993526"][0] != -0.5 {
		t.Fatalf("non-whitelisted venue unchanged, got %+v", sw["EURUSD"])
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
	if _, ok := fc.TradeMode("X", "v:mt5"); ok {
		t.Error("过期 trade_mode 查询应 ok=false")
	}
	// TTL 注入生效：放宽到 48h → 可见。
	fc.SetTradeModeTTL(48 * time.Hour)
	if mode, ok := fc.TradeMode("X", "v:mt5"); !ok || mode != fact.TradeModeFull {
		t.Errorf("TTL 48h 下应可见, got (%d, %v)", mode, ok)
	}
}
