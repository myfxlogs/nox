package exec

import (
	"testing"
	"time"

	"arb/internal/bus"
)

// revalidateBus 构造含双腿报价（同一 canonical）的 QuoteBus。
// 买腿 Bid/Ask 均取 buyAsk（mid=1.0），卖腿 Bid 取 sellBid → currentBps=(sellBid-buyAsk)/1.0×10000。
func revalidateBus(buyAsk, sellBid float64) *bus.QuoteBus {
	b := bus.New()
	now := time.Now()
	b.Publish(bus.Quote{Symbol: "EURUSD", Venue: "b", VenueType: "mt5", Bid: 1.0, Ask: buyAsk, Time: now, VenueSymbol: "EURUSDm"})
	b.Publish(bus.Quote{Symbol: "EURUSD", Venue: "s", VenueType: "mt5", Bid: sellBid, Ask: sellBid + 0.0002, Time: now, VenueSymbol: "EURUSDm"})
	return b
}

// TestRevalidateGrossDegradation 验证 Revalidate 毛口径比较（D-051 修③）：
// 双阈值 max(2bps 绝对, 20% 相对)，degradation = originalGrossBps − currentGrossBps。
func TestRevalidateGrossDegradation(t *testing.T) {
	t.Run("degradation within threshold passes", func(t *testing.T) {
		// currentBps=4（sellBid=1.0004），degradation=5-4=1 ≤ max(2, 1)=2 → 放行。
		p := &Pipeline{qbus: revalidateBus(1.0, 1.0004)}
		if !p.Revalidate("EURUSD", "b:mt5", "s:mt5", 5) {
			t.Errorf("degradation=1 within threshold want pass, got reject")
		}
	})

	t.Run("degradation beyond threshold rejected", func(t *testing.T) {
		// currentBps=2（sellBid=1.0002），degradation=5-2=3 > max(2, 1)=2 → 拒绝。
		p := &Pipeline{qbus: revalidateBus(1.0, 1.0002)}
		if p.Revalidate("EURUSD", "b:mt5", "s:mt5", 5) {
			t.Errorf("degradation=3 beyond threshold want reject, got pass")
		}
	})

	t.Run("missing quote rejected", func(t *testing.T) {
		p := &Pipeline{qbus: bus.New()}
		if p.Revalidate("EURUSD", "b:mt5", "s:mt5", 5) {
			t.Errorf("missing legs want reject, got pass")
		}
	})
}
