// d571c2_frameprice_test.go —— D-571 包C 补充锚（MT5 现价帧原子化）。
// T1 lastQuote 原生键落+publishPositions 帧内携价（mid+帧时戳；无报价仓零值诚实）；
// T2 双变体钉死（XAUUSDm primary 占 canonical 槽 / XAUUSD247m blocked 降格原生键，
//
//	两变体持仓各挂各的帧价，双向互不污染=2555274799 形态根治）。
package adapter

import (
	"context"
	"testing"

	"arb/internal/bus"
	"arb/internal/venue"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

// feedQuotes 驱 consumeQuoteFrames 消费一组报价帧并逐帧 drain（断言帧已入缓存后返回）。
func feedQuotes(t *testing.T, a *MT5Adapter, frames ...*mt5pb.Quote) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fch := make(chan *mt5pb.OnQuoteReply, len(frames))
	for _, q := range frames {
		fch <- &mt5pb.OnQuoteReply{Result: q}
	}
	out := make(chan bus.Quote, len(frames)+1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.consumeQuoteFrames(ctx, newFakeQuoteStream(ctx, fch, nil), out)
	}()
	for range frames {
		<-out // 每帧必产一条 bus.Quote——收到即证 lastQuote 已写
	}
	cancel()
	<-done
}

// captureSink 挂 PositionsSink 捕获最近一帧携价 venue.Order 集。
func captureSink(a *MT5Adapter) *[][]venue.Order {
	box := &[][]venue.Order{}
	a.PositionsSink = func(_ string, orders []venue.Order) { *box = append(*box, orders) }
	return box
}

func TestD571C2_FrameCarriedPrice(t *testing.T) {
	a := &MT5Adapter{venue: "v"}
	feedQuotes(t, a, &mt5pb.Quote{Symbol: "XAUUSDm", Bid: 2000.0, Ask: 2000.2})

	if v, ok := a.lastQuote.Load("XAUUSDm"); !ok || v.(bus.Quote).Bid != 2000.0 {
		t.Fatalf("lastQuote 应按原生键 XAUUSDm 落 bid=2000, ok=%v v=%v", ok, v)
	}
	box := captureSink(a)
	s := &MarginFactSource{a: a}
	s.publishPositions([]*mt5pb.Order{
		{Ticket: 1, Symbol: "XAUUSDm", OrderType: mt5pb.OrderType_OrderType_Buy, Lots: 0.1, OpenPrice: 1999.0, Profit: 1.1},
		{Ticket: 2, Symbol: "EURUSDm", OrderType: mt5pb.OrderType_OrderType_Buy, Lots: 0.1, OpenPrice: 1.08, Profit: 0.5}, // 无报价仓
	})
	if len(*box) != 1 || len((*box)[0]) != 2 {
		t.Fatalf("sink 应收 1 帧 2 仓, got %+v", box)
	}
	vo, vo2 := (*box)[0][0], (*box)[0][1]
	if vo.CurrentPrice != 2000.1 || vo.PriceAt.IsZero() {
		t.Fatalf("帧携价 want mid=2000.1+PriceAt, got %v@%v", vo.CurrentPrice, vo.PriceAt)
	}
	if vo2.CurrentPrice != 0 || !vo2.PriceAt.IsZero() {
		t.Fatalf("无报价仓应零值诚实空态, got %v@%v", vo2.CurrentPrice, vo2.PriceAt)
	}
}

// T2：双变体同 canonical——247m blocked 降格原生键发布仍携价命中；两仓互不污染。
func TestD571C2_DualVariantIsolation(t *testing.T) {
	a := &MT5Adapter{venue: "v"}
	a.SetSymbolMap(map[string]string{"XAUUSDm": "XAUUSD", "XAUUSD247m": "XAUUSD"})
	a.SetCanonicalEmitBlocked(map[string]bool{"XAUUSD247m": true})
	feedQuotes(t, a,
		&mt5pb.Quote{Symbol: "XAUUSDm", Bid: 2000.0, Ask: 2000.2},
		&mt5pb.Quote{Symbol: "XAUUSD247m", Bid: 3000.0, Ask: 3000.4},
	)
	// lastQuote 双侧原生键各自落槽。
	if _, ok := a.lastQuote.Load("XAUUSD247m"); !ok {
		t.Fatal("blocked 变体原生键应落 lastQuote")
	}
	if _, ok := a.lastQuote.Load("XAUUSDm"); !ok {
		t.Fatal("primary 变体原生键应落 lastQuote")
	}
	box := captureSink(a)
	s := &MarginFactSource{a: a}
	s.publishPositions([]*mt5pb.Order{
		{Ticket: 10, Symbol: "XAUUSDm", OrderType: mt5pb.OrderType_OrderType_Buy, Lots: 0.1, OpenPrice: 1999, Profit: 11},
		{Ticket: 11, Symbol: "XAUUSD247m", OrderType: mt5pb.OrderType_OrderType_Buy, Lots: 0.1, OpenPrice: 2900, Profit: 100},
	})
	vo1, vo2 := (*box)[0][0], (*box)[0][1]
	// 双向阴性断言：各行只挂自家变体帧价，绝不拿对方价。
	if vo1.CurrentPrice != 2000.1 {
		t.Fatalf("XAUUSDm 行应挂 2000.1（247m 价 3000.2 不得污染）, got %v", vo1.CurrentPrice)
	}
	if vo2.CurrentPrice != 3000.2 {
		t.Fatalf("XAUUSD247m 行应挂 3000.2（XAUUSDm 价 2000.1 不得污染）, got %v", vo2.CurrentPrice)
	}
}

// TestD571C2_FramePriceUnscaled D-594 冲突解语义锚：lastQuote 存**原始** bid/ask
// （Store 在 emitPlan 判定之前，不乘 price_scale）——帧携价=venue 原生价，与帧内
// PnL 同钟；发布面归一（scale 乘价）只作用于 ch 下行 bus.Quote，不回污帧携缓存。
func TestD571C2_FramePriceUnscaled(t *testing.T) {
	a := &MT5Adapter{venue: "v"}
	a.SetSymbolScales(map[string]SymbolScale{"XAUUSDm": {PriceScale: 100.0, QtyScale: 1}})
	feedQuotes(t, a, &mt5pb.Quote{Symbol: "XAUUSDm", Bid: 2000.0, Ask: 2000.2})

	v, ok := a.lastQuote.Load("XAUUSDm")
	if !ok {
		t.Fatal("lastQuote 未落")
	}
	q := v.(bus.Quote)
	if q.Bid != 2000.0 || q.Ask != 2000.2 {
		t.Fatalf("帧携价须原始值（不乘 scale=100）: bid=%v ask=%v", q.Bid, q.Ask)
	}
	// 阴性对照：若误乘 100，bid 会是 200000——上断言已覆盖；此处再钉发布面确实乘了
	// （证明两路分离而非 scale 失效）。
	s2 := &MT5Adapter{venue: "v"}
	s2.SetSymbolScales(map[string]SymbolScale{"XAUUSDm": {PriceScale: 100.0, QtyScale: 1}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fch := make(chan *mt5pb.OnQuoteReply, 1)
	fch <- &mt5pb.OnQuoteReply{Result: &mt5pb.Quote{Symbol: "XAUUSDm", Bid: 2000.0, Ask: 2000.2}}
	out := make(chan bus.Quote, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s2.consumeQuoteFrames(ctx, newFakeQuoteStream(ctx, fch, nil), out)
	}()
	pub := <-out
	cancel() // 流阻塞等 ctx——先取消再等收尾
	<-done
	if pub.Bid != 200000.0 {
		t.Fatalf("发布面应乘 scale=100（与帧携缓存分离）: bid=%v", pub.Bid)
	}
}
