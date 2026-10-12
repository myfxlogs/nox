// fact_cache_test.go —— D-060 F4：FactCache trade_mode 消费（venueKey 键口径）+ TTL 过期视为未知。
package engine

import (
	"testing"
	"time"

	"arb/internal/data/fact"
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

// TestFactCacheSnapshotIsolation P1#8b：读侧零拷贝快照语义——
// 写后新读者见到新值；先取走的旧快照不受后续写入影响（copy-on-write）。
// 注：trade_mode TTL 读侧过滤已退役（权威判定=exec 即时探针，过期条目无害可见）。
func TestFactCacheSnapshotIsolation(t *testing.T) {
	fc := NewFactCache()
	now := time.Now()
	fc.UpdateFacts([]fact.Fact{
		{Kind: fact.KindTradeMode, Venue: "v", Symbol: "X", Value: 4, Unit: fact.UnitMode, Ts: now, Src: "mt5"},
		{Kind: fact.KindFunding, Venue: "okx", Symbol: "BTCUSDT", Value: 5, Ts: now},
	})
	oldModes := fc.TradeModes()
	oldFunding := fc.FundingFacts()
	fc.UpdateFacts([]fact.Fact{
		{Kind: fact.KindTradeMode, Venue: "v", Symbol: "X", Value: 0, Unit: fact.UnitMode, Ts: now, Src: "mt5"},
		{Kind: fact.KindFunding, Venue: "binance", Symbol: "BTCUSDT", Value: 9, Ts: now},
	})
	if got := fc.TradeModes()["X"]["v:mt5"]; got != fact.TradeModeDisabled {
		t.Errorf("写后 TradeModes 应为 Disabled(0), got %d", got)
	}
	if got := oldModes["X"]["v:mt5"]; got != fact.TradeModeFull {
		t.Errorf("旧快照被后续写入污染: got %d, want Full(4)", got)
	}
	if _, ok := oldFunding["BTCUSDT"]["binance"]; ok {
		t.Error("旧 funding 快照被后续写入污染")
	}
	if got := fc.FundingFacts()["BTCUSDT"]["binance"]; got != 9 {
		t.Errorf("写后 FundingFacts 应含 binance=9, got %v", got)
	}
}

// TestFactCacheConcurrentReadWrite P1#8b：帧率级并发读 vs 写入重建（-race 门禁）。
func TestFactCacheConcurrentReadWrite(t *testing.T) {
	fc := NewFactCache()
	done := make(chan struct{})
	go func() {
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
				fc.UpdateFacts([]fact.Fact{
					{Kind: fact.KindFunding, Venue: "okx", Symbol: "BTCUSDT", Value: float64(i), Ts: time.Now()},
					{Kind: fact.KindTradeMode, Venue: "v", Symbol: "X", Value: 4, Unit: fact.UnitMode, Ts: time.Now(), Src: "mt5"},
				})
			}
		}
	}()
	for i := 0; i < 2000; i++ {
		_ = fc.FundingFacts()["BTCUSDT"]["okx"]
		_ = fc.TradeModes()["X"]["v:mt5"]
		_ = fc.SwapFacts()
		_ = fc.IsRealMap()
	}
	close(done)
}
