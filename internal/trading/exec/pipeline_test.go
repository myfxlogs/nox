package exec

import (
	"context"
	"testing"
	"time"

	"arb/internal/data/bus"
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
// FUNDING_HEDGE 跳过价差劣化（收益来自 funding rate 不是价格差）。
// 修复⑪B N4：阈值 env 化（revalidateAbsBps/revalidateRelPct 字段，默认 2.0/20）。
func TestRevalidateGrossDegradation(t *testing.T) {
	// newRevalidatePipeline 构造带默认阈值的 Pipeline（D-077 修复⑪B N4）
	newRevalidatePipeline := func(qbus *bus.QuoteBus) *Pipeline {
		return &Pipeline{qbus: qbus, revalidateAbsBps: 2.0, revalidateRelPct: 20.0}
	}
	t.Run("degradation within threshold passes", func(t *testing.T) {
		// currentBps=4（sellBid=1.0004），degradation=5-4=1 ≤ max(2, 1)=2 → 放行。
		p := newRevalidatePipeline(revalidateBus(1.0, 1.0004))
		if !p.Revalidate("EURUSD", "EURUSD", "b:mt5", "s:mt5", "CROSS_VENUE", 5) {
			t.Errorf("degradation=1 within threshold want pass, got reject")
		}
	})

	t.Run("degradation beyond threshold rejected", func(t *testing.T) {
		// currentBps=2（sellBid=1.0002），degradation=5-2=3 > max(2, 1)=2 → 拒绝。
		p := newRevalidatePipeline(revalidateBus(1.0, 1.0002))
		if p.Revalidate("EURUSD", "EURUSD", "b:mt5", "s:mt5", "CROSS_VENUE", 5) {
			t.Errorf("degradation=3 beyond threshold want reject, got pass")
		}
	})

	t.Run("missing quote rejected", func(t *testing.T) {
		p := newRevalidatePipeline(bus.New())
		if p.Revalidate("EURUSD", "EURUSD", "b:mt5", "s:mt5", "CROSS_VENUE", 5) {
			t.Errorf("missing legs want reject, got pass")
		}
	})

	t.Run("funding_hedge skips price degradation", func(t *testing.T) {
		// FUNDING_HEDGE：价格差近 0（sellBid=1.0001），originalGrossBps=10
		// CROSS_VENUE 会拒（degradation=10-1=9 > 2），但 FUNDING_HEDGE 应放行。
		p := newRevalidatePipeline(revalidateBus(1.0, 1.0001))
		if !p.Revalidate("EURUSD", "EURUSD", "b:mt5", "s:mt5", "FUNDING_HEDGE", 10) {
			t.Errorf("FUNDING_HEDGE should skip price degradation, got reject")
		}
	})

	t.Run("funding_hedge missing quote still rejected", func(t *testing.T) {
		p := newRevalidatePipeline(bus.New())
		if p.Revalidate("EURUSD", "EURUSD", "b:mt5", "s:mt5", "FUNDING_HEDGE", 10) {
			t.Errorf("FUNDING_HEDGE missing quote should reject, got pass")
		}
	})

	t.Run("perp_funding skips price degradation", func(t *testing.T) {
		// PERP_FUNDING 同 FUNDING_HEDGE——收益来自 funding rate 差不是价格差。
		// gate futures 只有 mark_price（bid=ask），价格差近 0 正常，不应误拒。
		p := newRevalidatePipeline(revalidateBus(1.0, 1.0001))
		if !p.Revalidate("EURUSD", "EURUSD", "b:mt5", "s:mt5", "PERP_FUNDING", 10) {
			t.Errorf("PERP_FUNDING should skip price degradation, got reject")
		}
	})

	t.Run("perp_funding missing quote still rejected", func(t *testing.T) {
		p := newRevalidatePipeline(bus.New())
		if p.Revalidate("EURUSD", "EURUSD", "b:mt5", "s:mt5", "PERP_FUNDING", 10) {
			t.Errorf("PERP_FUNDING missing quote should reject, got pass")
		}
	})

	// 锚：XASSET 跨 canonical——卖腿报价只挂 canonicalSell 命名空间时通过
	//（旧码 Latest(canonical, venueSell) 必 miss 误拒，生产实证 CLUSDT×gate:perp Abort）。
	t.Run("xasset_carry sell quote under canonicalSell passes", func(t *testing.T) {
		b := bus.New()
		now := time.Now()
		b.Publish(bus.Quote{Symbol: "CLUSDT", Venue: "277842155", VenueType: "mt5", Bid: 40, Ask: 40.01, Time: now, VenueSymbol: "CLUSDTm"})
		b.Publish(bus.Quote{Symbol: "WTI", Venue: "gate", VenueType: "perp", Bid: 40.5, Ask: 40.51, Time: now, VenueSymbol: "CL_USDT"})
		p := newRevalidatePipeline(b)
		if !p.Revalidate("CLUSDT", "WTI", "277842155:mt5", "gate:perp", "XASSET_CARRY", 23.2) {
			t.Errorf("XASSET_CARRY cross-canonical quotes present want pass, got reject")
		}
	})

	// 卖腿报价不在 canonicalSell 命名空间下 → 新鲜度检查仍拒（免检只免价差劣化）。
	t.Run("xasset_carry missing canonicalSell quote rejected", func(t *testing.T) {
		b := bus.New()
		now := time.Now()
		b.Publish(bus.Quote{Symbol: "CLUSDT", Venue: "277842155", VenueType: "mt5", Bid: 40, Ask: 40.01, Time: now, VenueSymbol: "CLUSDTm"})
		b.Publish(bus.Quote{Symbol: "CLUSDT", Venue: "gate", VenueType: "perp", Bid: 40.5, Ask: 40.51, Time: now, VenueSymbol: "X"})
		p := newRevalidatePipeline(b)
		if p.Revalidate("CLUSDT", "WTI", "277842155:mt5", "gate:perp", "XASSET_CARRY", 23.2) {
			t.Errorf("sell quote absent under canonicalSell want reject, got pass")
		}
	})

	// canonicalSell=canonicalBuy（调用方 NULL/空回退结果）→ 与旧同 canonical 行为一致。
	t.Run("canonicalSell fallback same canonical", func(t *testing.T) {
		p := newRevalidatePipeline(revalidateBus(1.0, 1.0004))
		if !p.Revalidate("EURUSD", "EURUSD", "b:mt5", "s:mt5", "CROSS_VENUE", 5) {
			t.Errorf("same-canonical fallback want pass, got reject")
		}
	})
}

// captureOrderExecutor 捕获 exec.Order 的测试执行器（D-311 R4 执行器边界单位断言）。
type captureOrderExecutor struct {
	key  string
	got  Order
	fill Fill
}

func (e *captureOrderExecutor) Key() string { return e.key }
func (e *captureOrderExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	e.got = o
	return e.fill, nil
}
func (e *captureOrderExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return e.fill, nil
}

// TestD311R4_ExecuteLeg_PerpQtyContractToBase D-311 R4 执行器边界换算锚——
// pipeline 口径 :perp 腿 qty=合约数（张），Order.Qty 契约=基础币 → ×cs。
// 生产实证：gate CL_USDT 意图 ~$1000 → 1575 张被当 1575 桶直送 → size=157500 张（放大 100 倍）。
func TestD311R4_ExecuteLeg_PerpQtyContractToBase(t *testing.T) {
	reg := NewRegistry()
	gate := &captureOrderExecutor{key: "gate:perp", fill: Fill{OrderID: "g1", Price: 63.5, Qty: 15.75}}
	okx := &captureOrderExecutor{key: "okx:perp", fill: Fill{OrderID: "o1", Price: 63.5, Qty: 20}}
	bnc := &captureOrderExecutor{key: "binance:perp", fill: Fill{OrderID: "b1", Price: 50000, Qty: 1.5}}
	mt := &captureOrderExecutor{key: "3001157:mt5", fill: Fill{OrderID: "m1", Price: 63.5, Qty: 0.01}}
	reg.Register(gate)
	reg.Register(okx)
	reg.Register(bnc)
	reg.Register(mt)
	p := &Pipeline{registry: reg, csResolver: &fakeCSResolver{sizes: map[string]float64{
		"gate:perp:CL_USDT":     0.01,
		"okx:perp:CL-USDT-SWAP": 10,
		"binance:perp:BTCUSDT":  1,
	}}}

	// gate:perp end-to-end sizing：1575 张 × quanto 0.01 → Order.Qty=15.75 基础币
	r := p.executeLeg(context.Background(), LegSpec{VenueKey: "gate:perp", VenueSymbol: "CL_USDT", Side: "BUY", Qty: 1575}, "c1")
	if r.Status != "FILLED" || gate.got.Qty != 15.75 {
		t.Fatalf("gate:perp status=%s Order.Qty=%v，期望 FILLED/15.75", r.Status, gate.got.Qty)
	}
	// okx:perp 2 张 × ctVal 10 → Order.Qty=20
	r = p.executeLeg(context.Background(), LegSpec{VenueKey: "okx:perp", VenueSymbol: "CL-USDT-SWAP", Side: "SELL", Qty: 2}, "c2")
	if r.Status != "FILLED" || okx.got.Qty != 20 {
		t.Fatalf("okx:perp status=%s Order.Qty=%v，期望 FILLED/20", r.Status, okx.got.Qty)
	}
	// binance:perp ct_val=1 → ×1 不变
	r = p.executeLeg(context.Background(), LegSpec{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "BUY", Qty: 1.5}, "c3")
	if r.Status != "FILLED" || bnc.got.Qty != 1.5 {
		t.Fatalf("binance:perp status=%s Order.Qty=%v，期望 FILLED/1.5", r.Status, bnc.got.Qty)
	}
	// MT 腿不换算（qty=手数直送）
	r = p.executeLeg(context.Background(), LegSpec{VenueKey: "3001157:mt5", VenueSymbol: "CLUSDm", Side: "SELL", Qty: 0.01}, "c4")
	if r.Status != "FILLED" || mt.got.Qty != 0.01 {
		t.Fatalf("mt5 status=%s Order.Qty=%v，期望 FILLED/0.01", r.Status, mt.got.Qty)
	}
	// :perp 腿 cs 缺失 → fail-closed
	r = p.executeLeg(context.Background(), LegSpec{VenueKey: "gate:perp", VenueSymbol: "ZZZ_USDT", Side: "BUY", Qty: 10}, "c5")
	if r.Status != "FAILED" {
		t.Fatalf("cs 缺失 status=%s，期望 FAILED", r.Status)
	}
	// csResolver 未接线 → fail-closed
	p2 := &Pipeline{registry: reg}
	r = p2.executeLeg(context.Background(), LegSpec{VenueKey: "gate:perp", VenueSymbol: "CL_USDT", Side: "BUY", Qty: 10}, "c6")
	if r.Status != "FAILED" {
		t.Fatalf("csResolver 未接线 status=%s，期望 FAILED", r.Status)
	}
}
