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
// D-068：FUNDING_HEDGE 跳过价差劣化（收益来自 funding rate 不是价格差）。
// D-077 修复⑪B N4：阈值 env 化（revalidateAbsBps/revalidateRelPct 字段，默认 2.0/20）。
func TestRevalidateGrossDegradation(t *testing.T) {
	// newRevalidatePipeline 构造带默认阈值的 Pipeline（D-077 修复⑪B N4）
	newRevalidatePipeline := func(qbus *bus.QuoteBus) *Pipeline {
		return &Pipeline{qbus: qbus, revalidateAbsBps: 2.0, revalidateRelPct: 20.0}
	}
	t.Run("degradation within threshold passes", func(t *testing.T) {
		// currentBps=4（sellBid=1.0004），degradation=5-4=1 ≤ max(2, 1)=2 → 放行。
		p := newRevalidatePipeline(revalidateBus(1.0, 1.0004))
		if !p.Revalidate("EURUSD", "b:mt5", "s:mt5", "CROSS_VENUE", 5) {
			t.Errorf("degradation=1 within threshold want pass, got reject")
		}
	})

	t.Run("degradation beyond threshold rejected", func(t *testing.T) {
		// currentBps=2（sellBid=1.0002），degradation=5-2=3 > max(2, 1)=2 → 拒绝。
		p := newRevalidatePipeline(revalidateBus(1.0, 1.0002))
		if p.Revalidate("EURUSD", "b:mt5", "s:mt5", "CROSS_VENUE", 5) {
			t.Errorf("degradation=3 beyond threshold want reject, got pass")
		}
	})

	t.Run("missing quote rejected", func(t *testing.T) {
		p := newRevalidatePipeline(bus.New())
		if p.Revalidate("EURUSD", "b:mt5", "s:mt5", "CROSS_VENUE", 5) {
			t.Errorf("missing legs want reject, got pass")
		}
	})

	t.Run("funding_hedge skips price degradation", func(t *testing.T) {
		// FUNDING_HEDGE：价格差近 0（sellBid=1.0001），originalGrossBps=10
		// CROSS_VENUE 会拒（degradation=10-1=9 > 2），但 FUNDING_HEDGE 应放行。
		p := newRevalidatePipeline(revalidateBus(1.0, 1.0001))
		if !p.Revalidate("EURUSD", "b:mt5", "s:mt5", "FUNDING_HEDGE", 10) {
			t.Errorf("FUNDING_HEDGE should skip price degradation, got reject")
		}
	})

	t.Run("funding_hedge missing quote still rejected", func(t *testing.T) {
		p := newRevalidatePipeline(bus.New())
		if p.Revalidate("EURUSD", "b:mt5", "s:mt5", "FUNDING_HEDGE", 10) {
			t.Errorf("FUNDING_HEDGE missing quote should reject, got pass")
		}
	})
}
