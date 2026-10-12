// mt_order_events_test.go —— D-107 P1（2026-08-25）：OnOrderUpdate 流消费单测。
// 三用例（先红后绿）：
//   - EventPublished：注入 1 帧 OrderUpdate（Order.Ticket=12345）→ sink 收到 OrderEvent{Venue, Ticket=12345}；
//   - StreamReconnect：stream1 Recv 返回 Unavailable（IsSessionLost=true）→ reconnectSession →
//     退避 → 重订阅 → stream2 帧照常触发 sink；
//   - SinkPanicNoCrash：sink panic → recover+日志，后续帧照常处理（事件丢失可接受，不拉崩进程）。
//
// fakeMT5Client（mt_swap_poll_test.go）已内嵌 SubscriptionsClient + StreamsClient，
// 此处补 SubscribeOrderUpdate / OnOrderUpdate 覆盖 + fakeOrderUpdateStream。
package adapter

import (
	"context"
	"sync"
	"testing"
	"time"

	"nox/internal/data/bus"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
)

// SubscribeOrderUpdate 覆盖（D-107 P1）：返回 OK（subsOUErr 时返回 err），计数供断言。
func (f *fakeMT5Client) SubscribeOrderUpdate(_ context.Context, _ *mt5pb.SubscribeOrderUpdateRequest, _ ...grpc.CallOption) (*mt5pb.SubscribeOrderUpdateReply, error) {
	f.subsOUCalls++
	if f.subsOUErr != nil {
		return nil, f.subsOUErr
	}
	return &mt5pb.SubscribeOrderUpdateReply{}, nil
}

// OnOrderUpdate 覆盖（D-107 P1）：按队列弹出预设流；onOUErr 一次性优先；队列空 → 阻塞流。
// 预设流创建时用 Background（测试 setup 早于 ctx），此处回填调用 ctx 使 Recv 在 ctx
// 取消时能解锁（同 D-099 OnOrderProfit 模式）。
func (f *fakeMT5Client) OnOrderUpdate(ctx context.Context, _ *mt5pb.OnOrderUpdateRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[mt5pb.OnOrderUpdateReply], error) {
	f.onOUCalls++
	if f.onOUErr != nil {
		err := f.onOUErr
		f.onOUErr = nil // 一次性
		return nil, err
	}
	if f.onOUIdx < len(f.onOUStreams) {
		s := f.onOUStreams[f.onOUIdx]
		f.onOUIdx++
		if fs, ok := s.(*fakeOrderUpdateStream); ok {
			fs.ctx = ctx // 回填调用 ctx（派生自测试 ctx），Recv 解锁依赖此
		}
		return s, nil
	}
	return newFakeOrderUpdateStream(ctx, nil, nil), nil
}

// fakeOrderUpdateStream 可控 OnOrderUpdate 流：frames 缓冲帧 / recvErr 一次性注入错误 / 空则阻塞 ctx。
type fakeOrderUpdateStream struct {
	ctx     context.Context
	frames  chan *mt5pb.OnOrderUpdateReply
	recvErr error // 一次性：无帧时返回此错（返回后视为已发，不再重复）
	errSent bool
	hdr, tr metadata.MD
}

func newFakeOrderUpdateStream(ctx context.Context, frames chan *mt5pb.OnOrderUpdateReply, recvErr error) *fakeOrderUpdateStream {
	if frames == nil {
		frames = make(chan *mt5pb.OnOrderUpdateReply, 4)
	}
	return &fakeOrderUpdateStream{ctx: ctx, frames: frames, recvErr: recvErr}
}

func (s *fakeOrderUpdateStream) Recv() (*mt5pb.OnOrderUpdateReply, error) {
	select {
	case f := <-s.frames:
		return f, nil
	default:
	}
	if s.recvErr != nil && !s.errSent {
		s.errSent = true
		return nil, s.recvErr
	}
	select {
	case f := <-s.frames:
		return f, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func (s *fakeOrderUpdateStream) Header() (metadata.MD, error) { return s.hdr, nil }
func (s *fakeOrderUpdateStream) Trailer() metadata.MD         { return s.tr }
func (s *fakeOrderUpdateStream) CloseSend() error             { return nil }
func (s *fakeOrderUpdateStream) Context() context.Context     { return s.ctx }
func (s *fakeOrderUpdateStream) SendMsg(_ interface{}) error  { return nil }
func (s *fakeOrderUpdateStream) RecvMsg(_ interface{}) error  { return nil }

// 编译期断言：fakeOrderUpdateStream 实现 grpc.ServerStreamingClient[mt5pb.OnOrderUpdateReply]。
var _ grpc.ServerStreamingClient[mt5pb.OnOrderUpdateReply] = (*fakeOrderUpdateStream)(nil)

// withShortOrderEventBackoff 临时缩短 order-event 重连退避，返回恢复函数（测试快跑）。
func withShortOrderEventBackoff(t *testing.T, recon []time.Duration) func() {
	t.Helper()
	orig := marginReconnectBackoff
	marginReconnectBackoff = recon
	return func() { marginReconnectBackoff = orig }
}

// startOrderEventSource 启动 OrderEventSource.Run 并返回 sink 通道 + cleanup。
// cleanup 取消 ctx 并等待 Run goroutine 退出（同 startMarginSource 模式）。
func startOrderEventSource(t *testing.T, src *OrderEventSource) (chan bus.OrderEvent, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sinkCh := make(chan bus.OrderEvent, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		src.Run(ctx, func(e bus.OrderEvent) { sinkCh <- e })
	}()
	return sinkCh, func() {
		cancel()
		<-done
	}
}

// startOrderEventSourceWithSink 启动 OrderEventSource.Run 并使用自定义 sink + cleanup。
func startOrderEventSourceWithSink(t *testing.T, src *OrderEventSource, sink func(bus.OrderEvent)) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		src.Run(ctx, sink)
	}()
	return func() {
		cancel()
		<-done
	}
}

// TestOrderEventSource_EventPublished 注入 1 帧 OrderUpdate（Order.Ticket=12345）→
// sink 收到 OrderEvent{Venue="v7", Ticket=12345}。Update==nil 帧 skip 不触发 sink。
func TestOrderEventSource_EventPublished(t *testing.T) {
	client := &fakeMT5Client{}
	a := &MT5Adapter{venue: "v7", mt5: client, subs: client, streams: client, token: "tok"}

	frameCh := make(chan *mt5pb.OnOrderUpdateReply, 4)
	// 第 1 帧：Update==nil → skip。
	frameCh <- &mt5pb.OnOrderUpdateReply{Result: &mt5pb.OrderUpdateSummary{}}
	// 第 2 帧：Order.Ticket=12345 → 发布。
	frameCh <- &mt5pb.OnOrderUpdateReply{Result: &mt5pb.OrderUpdateSummary{
		Update: &mt5pb.OrderUpdate{Order: &mt5pb.Order{Ticket: 12345}},
	}}
	client.onOUStreams = []grpc.ServerStreamingClient[mt5pb.OnOrderUpdateReply]{
		newFakeOrderUpdateStream(context.Background(), frameCh, nil),
	}

	src := &OrderEventSource{a: a}
	sinkCh, cleanup := startOrderEventSource(t, src)
	defer cleanup()

	select {
	case e := <-sinkCh:
		if e.Venue != "v7" {
			t.Errorf("Venue = %q, want v7", e.Venue)
		}
		if e.Ticket != 12345 {
			t.Errorf("Ticket = %d, want 12345", e.Ticket)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("order event not received within 2s")
	}
	// SubscribeOrderUpdate 1 次 + OnOrderUpdate 1 次。
	if client.subsOUCalls != 1 {
		t.Errorf("SubscribeOrderUpdate calls = %d, want 1", client.subsOUCalls)
	}
	if client.onOUCalls != 1 {
		t.Errorf("OnOrderUpdate calls = %d, want 1", client.onOUCalls)
	}
}

// TestOrderEventSource_StreamReconnect 流先注入错误再重开 → 重订阅后帧照常触发 sink。
// stream1 Recv 返回 Unavailable（IsSessionLost=true）→ reconnectSession（fakeConnMgr）
// → 退避 → 重订阅 → stream2 帧触发 sink。
func TestOrderEventSource_StreamReconnect(t *testing.T) {
	restore := withShortOrderEventBackoff(t, []time.Duration{10 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond})
	defer restore()

	client := &fakeMT5Client{}
	connMgr := &fakeConnMgr{}
	a := &MT5Adapter{venue: "v8", mt5: client, subs: client, streams: client, token: "tok", connMgr: connMgr}

	// stream1：Recv 立即返回 Unavailable（会话失效）。
	stream1 := newFakeOrderUpdateStream(context.Background(), nil, status.Error(codes.Unavailable, "rst stream"))
	// stream2：1 帧后阻塞。
	frameCh := make(chan *mt5pb.OnOrderUpdateReply, 4)
	frameCh <- &mt5pb.OnOrderUpdateReply{Result: &mt5pb.OrderUpdateSummary{
		Update: &mt5pb.OrderUpdate{Order: &mt5pb.Order{Ticket: 99}},
	}}
	stream2 := newFakeOrderUpdateStream(context.Background(), frameCh, nil)
	client.onOUStreams = []grpc.ServerStreamingClient[mt5pb.OnOrderUpdateReply]{stream1, stream2}

	src := &OrderEventSource{a: a}
	sinkCh, cleanup := startOrderEventSource(t, src)
	defer cleanup()

	select {
	case e := <-sinkCh:
		if e.Ticket != 99 {
			t.Errorf("Ticket = %d, want 99 (post-reconnect)", e.Ticket)
		}
		if e.Venue != "v8" {
			t.Errorf("Venue = %q, want v8", e.Venue)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("post-reconnect event not received within 3s")
	}
	// 重订阅 2 次（stream1 断后重开 stream2）。
	if client.subsOUCalls != 2 {
		t.Errorf("SubscribeOrderUpdate calls = %d, want 2 (initial + reconnect)", client.subsOUCalls)
	}
	if client.onOUCalls != 2 {
		t.Errorf("OnOrderUpdate calls = %d, want 2 (stream1 err + stream2 ok)", client.onOUCalls)
	}
	if connMgr.connectCallsLoad() != 1 {
		t.Errorf("connMgr.Connect calls = %d, want 1 (session lost → reconnect)", connMgr.connectCallsLoad())
	}
}

// TestOrderEventSource_SinkPanicNoCrash sink panic → recover+日志，后续帧照常处理。
// 第 1 帧 sink panic → recover；第 2 帧 sink 正常 → 收到事件（证明 source 未崩溃）。
func TestOrderEventSource_SinkPanicNoCrash(t *testing.T) {
	client := &fakeMT5Client{}
	a := &MT5Adapter{venue: "v9", mt5: client, subs: client, streams: client, token: "tok"}

	frameCh := make(chan *mt5pb.OnOrderUpdateReply, 4)
	frameCh <- &mt5pb.OnOrderUpdateReply{Result: &mt5pb.OrderUpdateSummary{
		Update: &mt5pb.OrderUpdate{Order: &mt5pb.Order{Ticket: 1}},
	}}
	frameCh <- &mt5pb.OnOrderUpdateReply{Result: &mt5pb.OrderUpdateSummary{
		Update: &mt5pb.OrderUpdate{Order: &mt5pb.Order{Ticket: 2}},
	}}
	client.onOUStreams = []grpc.ServerStreamingClient[mt5pb.OnOrderUpdateReply]{
		newFakeOrderUpdateStream(context.Background(), frameCh, nil),
	}

	src := &OrderEventSource{a: a}

	var mu sync.Mutex
	var got []int64
	sink := func(e bus.OrderEvent) {
		mu.Lock()
		defer mu.Unlock()
		if e.Ticket == 1 {
			panic("simulated sink failure")
		}
		got = append(got, e.Ticket)
	}

	cleanup := startOrderEventSourceWithSink(t, src, sink)
	defer cleanup()

	// 等待第 2 帧被处理（Ticket=2 入 got 证明 source 在 panic 后继续运行）。
	if !waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) > 0 && got[0] == 2
	}) {
		mu.Lock()
		t.Fatalf("sink did not receive ticket=2 after panic recovery, got=%v", got)
	}
}

// TestOrderEventSource_AcctSinkTap D-445 S0：OrderUpdateSummary 余额帧每帧下沉（含 Update==nil 纯余额帧），
// 订单事件 sink 仍只在 Update!=nil 触发；六字段+收帧时刻正确；acct sink panic 不拉崩流循环。
func TestOrderEventSource_AcctSinkTap(t *testing.T) {
	client := &fakeMT5Client{}
	a := &MT5Adapter{venue: "v10", mt5: client, subs: client, streams: client, token: "tok"}

	frameCh := make(chan *mt5pb.OnOrderUpdateReply, 4)
	// 帧1：Update==nil 纯余额帧 → acct sink 触发、order sink 不触发。
	frameCh <- &mt5pb.OnOrderUpdateReply{Result: &mt5pb.OrderUpdateSummary{
		Balance: 1000.5, Equity: 1100.25, Margin: 50, FreeMargin: 1050.25, Profit: 99.75, MarginLevel: 2200,
	}}
	// 帧2：acct sink panic → recover，流循环不崩。
	frameCh <- &mt5pb.OnOrderUpdateReply{Result: &mt5pb.OrderUpdateSummary{Balance: 1}}
	// 帧3：Update!=nil → 两 sink 各触发一次。
	frameCh <- &mt5pb.OnOrderUpdateReply{Result: &mt5pb.OrderUpdateSummary{
		Balance: 2000, Equity: 2100,
		Update: &mt5pb.OrderUpdate{Order: &mt5pb.Order{Ticket: 777}},
	}}
	client.onOUStreams = []grpc.ServerStreamingClient[mt5pb.OnOrderUpdateReply]{
		newFakeOrderUpdateStream(context.Background(), frameCh, nil),
	}

	src := &OrderEventSource{a: a}
	type acctCall struct {
		venue   string
		bal, eq float64
		ft      time.Time
	}
	var mu sync.Mutex
	var calls []acctCall
	before := time.Now()
	src.SetAccountSink(func(vk string, balance, equity, margin, freeMargin, profit, marginLevel float64, currency string, frameTime time.Time) {
		mu.Lock()
		defer mu.Unlock()
		if balance == 1 {
			panic("simulated acct sink failure")
		}
		calls = append(calls, acctCall{vk, balance, equity, frameTime})
	})

	sinkCh, cleanup := startOrderEventSource(t, src)
	defer cleanup()

	// order sink 只收 ticket=777 一条（Update==nil 帧跳过）。
	select {
	case e := <-sinkCh:
		if e.Ticket != 777 {
			t.Fatalf("order event ticket=%d, want 777", e.Ticket)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("order event not received")
	}

	if !waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(calls) == 2
	}) {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("acct calls=%v, want 2 (panic frame recovered, not counted)", calls)
	}
	mu.Lock()
	defer mu.Unlock()
	c0, c1 := calls[0], calls[1]
	if c0.venue != "v10" || c0.bal != 1000.5 || c0.eq != 1100.25 {
		t.Fatalf("frame1 call=%+v", c0)
	}
	if c1.bal != 2000 || c1.eq != 2100 {
		t.Fatalf("frame3 call=%+v", c1)
	}
	for _, c := range calls {
		if c.ft.Before(before) || c.ft.After(time.Now()) {
			t.Fatalf("frameTime=%v not ≈recv now", c.ft)
		}
	}
}
