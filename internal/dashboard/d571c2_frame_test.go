// d571c2_frame_test.go —— D-571 包C 补充锚（dashboard 侧）。
// T3 onQuote 原生键命中（CEX/MT4 行 pb.Symbol==q.VenueSymbol）+mt5 死踢收束；
// T4 REST 覆盖保护（REST 行 PriceAt=0 不冲帧携价；下一帧新价自然替换）；
// T5 帧一致性锚（帧携价行 CurrentPrice 隐含方向与 Pnl 符号一致+价差≤点差量级）。
package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/reconcile"

	v1 "arb/internal/gen/dashboardv1"
)

// T3a：onQuote 原生 brokerSym 命中——CEX 行 pb.Symbol==q.VenueSymbol 脏化（变体/原生行）。
func TestD571C2_OnQuoteNativeKeyHit(t *testing.T) {
	ctx := context.Background()
	q := &fakeCEXQuerier{key: "okx:perp", orders: []reconcile.VenueOrder{
		{Venue: "okx", VenueKey: "okx:perp", Ticket: "P1", Symbol: "XAU-USDT-SWAP", Side: "SELL", Volume: 1, OpenPrice: 2000},
	}}
	m, ch, _ := newEvMonTest(t, q)
	m.Refresh(ctx)
	drainEvents(ch)
	// canonical 槽价（q.Symbol=XAUUSDT 归一键）命中 —— 现路径回归。
	m.onQuote(bus.Quote{Symbol: "XAUUSDT", Venue: "okx", VenueType: "perp", Bid: 1, Ask: 1.01, Time: time.Now()})
	m.flush(ctx)
	if n := len(drainEvents(ch)); n != 0 {
		t.Fatalf("同值报价应无事件, got %d", n)
	}
	// 原生 brokerSym 匹配臂：读侧价格帧 Symbol=归一键 XAUUSDT（LatestLeg 命中源），
	// 脏化帧 Symbol=原生降格键（旧匹配式 Canonical==Symbol 与 Cex 归一均 miss，
	// 仅 pb.Symbol==q.VenueSymbol 命中）——钉死新匹配臂。
	m.qbus.Publish(bus.Quote{Symbol: "XAUUSDT", Venue: "okx", VenueType: "perp", Bid: 2001, Ask: 2002, Time: time.Now()})
	m.onQuote(bus.Quote{Symbol: "XAU-USDT-SWAP", Venue: "okx", VenueType: "perp", VenueSymbol: "XAU-USDT-SWAP", Bid: 2001, Ask: 2002, Time: time.Now()})
	m.flush(ctx)
	found := false
	for _, ev := range drainEvents(ch) {
		if u := ev.GetUpsert(); u != nil && u.Ticket == "P1" && u.CurrentPrice == 2001.5 {
			found = true
		}
	}
	if !found {
		t.Fatal("原生键报价应脏化持仓行并发 upsert（CurrentPrice=2001.5）")
	}
}

// T3b：onQuote mt5 死踢收束——mt5 行价只认帧携，quote 不再脏化（零事件）。
func TestD571C2_OnQuoteMT5DeadKick(t *testing.T) {
	ctx := context.Background()
	q := &fakeVenueQuerier{key: "v:mt5", orders: []reconcile.VenueOrder{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "EURUSDm", Side: "BUY", Volume: 0.1, OpenPrice: 1.1},
	}}
	m, ch, _ := newEvMonTest(t, q)
	m.Refresh(ctx)
	drainEvents(ch)
	m.onQuote(bus.Quote{Symbol: "EURUSD", Venue: "v", VenueType: "mt5", VenueSymbol: "EURUSDm", Bid: 1.2, Ask: 1.21, Time: time.Now()})
	m.flush(ctx)
	if n := len(drainEvents(ch)); n != 0 {
		t.Fatalf("mt5 行 quote 应死踢（零事件）, got %d", n)
	}
}

// T4：REST 回读不冲帧携价——帧携价行被 REST 覆盖后 PriceAt/CurrentPrice 保留；
// 下一帧携新价自然替换。
func TestD571C2_RestPreservesFramePrice(t *testing.T) {
	ctx := context.Background()
	q := &fakeVenueQuerier{key: "v:mt5", orders: []reconcile.VenueOrder{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "EURUSDm", Side: "BUY", Volume: 0.1, OpenPrice: 1.1},
	}}
	m, ch, _ := newEvMonTest(t, q)
	m.Refresh(ctx)
	drainEvents(ch)
	// 帧携价入场（CurrentPrice=1.1010，PriceAt=now）。
	frameTs := time.Now()
	m.OnMT5Frame("v:mt5", []reconcile.VenueOrder{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "EURUSDm", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Profit: 1, CurrentPrice: 1.1010, PriceAt: frameTs},
	})
	m.flush(ctx)
	drainEvents(ch)
	// REST 覆盖（querier 行 PriceAt=0）→ 帧携价应保留。
	m.reconcileVenue(ctx, "v:mt5")
	m.flush(ctx)
	var pb *v1.BrokerPosition
	for _, p := range m.Snapshot() {
		if p.Ticket == "1" {
			pb = p
		}
	}
	if pb == nil || pb.CurrentPrice != 1.1010 || !pb.Fresh {
		t.Fatalf("REST 不应冲帧携价, got %+v", pb)
	}
	// 下一帧携新价 → 自然替换。
	m.OnMT5Frame("v:mt5", []reconcile.VenueOrder{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "EURUSDm", Side: "BUY", Volume: 0.1, OpenPrice: 1.1, Profit: 2, CurrentPrice: 1.1050, PriceAt: time.Now()},
	})
	m.flush(ctx)
	for _, p := range m.Snapshot() {
		if p.Ticket == "1" && p.CurrentPrice != 1.1050 {
			t.Fatalf("下一帧应替换携价, got %v", p.CurrentPrice)
		}
	}
}

// T5：帧一致性锚——帧携价行 CurrentPrice 与 Pnl 同帧（隐含方向一致+价差≤点差量级）。
func TestD571C2_FramePriceConsistency(t *testing.T) {
	ctx := context.Background()
	q := &fakeVenueQuerier{key: "v:mt5", orders: []reconcile.VenueOrder{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "XAUUSDm", Side: "BUY", Volume: 0.1, OpenPrice: 1999.0},
	}}
	m, _, _ := newEvMonTest(t, q)
	m.Refresh(ctx)
	// 帧携价（mid=2000.1，点差 0.2）+同帧 PnL 正值——方向一致。
	m.OnMT5Frame("v:mt5", []reconcile.VenueOrder{
		{Venue: "v", VenueKey: "v:mt5", Ticket: "1", Symbol: "XAUUSDm", Side: "BUY", Volume: 0.1, OpenPrice: 1999.0, Profit: 11, CurrentPrice: 2000.1, PriceAt: time.Now()},
	})
	m.flush(ctx) // 帧 patch 脏化 → flush 重组装（protoFor 帧携价短接路径）
	var pb *v1.BrokerPosition
	for _, p := range m.Snapshot() {
		if p.Ticket == "1" {
			pb = p
		}
	}
	if pb == nil || !pb.Fresh || pb.CurrentPrice != 2000.1 {
		t.Fatalf("帧携价应透出 mid=2000.1 fresh, got %+v", pb)
	}
	implied := pb.CurrentPrice - pb.OpenPrice // BUY：现价>开价 → 浮盈为正
	if (implied > 0) != (pb.Pnl > 0) {
		t.Fatalf("隐含方向 %+v 与 Pnl %v 应同号（同帧一致）", implied, pb.Pnl)
	}
	if d := pb.CurrentPrice - pb.OpenPrice - 1.1; d > 0.2 || d < -0.2 { // 隐含价差偏离≤点差量级（fake 点差 0.2）
		t.Fatalf("隐含价差偏离超点差量级: %v", d)
	}
}
