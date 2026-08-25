// mt_quote_stream_test.go —— D-100（2026-08-25）：OnQuote 流断重连单测。
// 四用例（先红后绿）：
//   - ReconnectAfterRecvErr：注入 1 帧 → Recv 返 Unavailable err → 重连退避 → 第 2 条流继续投递帧到 ch；
//   - ResubscribeSurvivesReconnect：Subscribe([A]) → Resubscribe([B]) → 触发重连 → 断言 resubscribeAll 调用集 = {A,B}；
//   - CtxCancelClosesCh：ctx 取消 → ch 关闭（_, ok := <-ch ok=false）；
//   - SessionLostTriggersReconnect：Recv 返 "Value cannot be null" → 断言 connMgr.Connect 调用计数+1。
//
// fakeMT5Client（mt_swap_poll_test.go）已内嵌 SubscriptionsClient + StreamsClient，
// 此处补 OnQuote / Subscribe 覆盖 + fakeQuoteStream（同 fakeOrderProfitStream 模式）。
package adapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"arb/internal/bus"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// --- fakeMT5Client 补覆盖（D-100 OnQuote 流测试） ---

// Subscribe 覆盖（D-100）：记录被订阅符号（resubscribeAll 断言用），返回 OK。
// subCalls/subscribedSymbols 归 subMu 保护（并发 Resubscribe 与 resubscribeAll）。
func (f *fakeMT5Client) Subscribe(_ context.Context, req *mt5pb.SubscribeRequest, _ ...grpc.CallOption) (*mt5pb.SubscribeReply, error) {
	f.subMu.Lock()
	if f.subscribedSymbols == nil {
		f.subscribedSymbols = make(map[string]bool)
	}
	f.subscribedSymbols[req.GetSymbol()] = true
	f.subCalls++
	f.subMu.Unlock()
	if f.subErr != nil {
		return nil, f.subErr
	}
	return &mt5pb.SubscribeReply{}, nil
}

// OnQuote 覆盖（D-100）：按队列弹出预设流；onQuoteErr 一次性优先；队列空 → 阻塞流。
// onQuoteCalls/onQuoteIdx/onQuoteStreams 归 onQuoteMu 保护（runQuoteStream 与 waitFor 断言并发）。
// 预设流创建时用 Background（测试 setup 早于 ctx），此处回填调用 ctx 使 Recv 在 ctx
// 取消时能解锁（同 OnOrderProfit 模式）。
func (f *fakeMT5Client) OnQuote(ctx context.Context, _ *mt5pb.OnQuoteRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[mt5pb.OnQuoteReply], error) {
	f.onQuoteMu.Lock()
	f.onQuoteCalls++
	if f.onQuoteErr != nil {
		err := f.onQuoteErr
		f.onQuoteErr = nil // 一次性
		f.onQuoteMu.Unlock()
		return nil, err
	}
	var s grpc.ServerStreamingClient[mt5pb.OnQuoteReply]
	if f.onQuoteIdx < len(f.onQuoteStreams) {
		s = f.onQuoteStreams[f.onQuoteIdx]
		f.onQuoteIdx++
	}
	f.onQuoteMu.Unlock()
	if s != nil {
		if fs, ok := s.(*fakeQuoteStream); ok {
			fs.ctx = ctx // 回填调用 ctx（派生自测试 ctx），Recv 解锁依赖此
		}
		return s, nil
	}
	// 默认阻塞流（无帧无错，阻塞于 ctx）——仅关心 ctx 取消的测试用。
	return newFakeQuoteStream(ctx, nil, nil), nil
}

// onQuoteCallsLoad 原子读 onQuoteCalls（waitFor 断言用，避免直接读触发 race）。
func (f *fakeMT5Client) onQuoteCallsLoad() int {
	f.onQuoteMu.Lock()
	defer f.onQuoteMu.Unlock()
	return f.onQuoteCalls
}

// fakeQuoteStream 可控 OnQuote 流：frames 缓冲帧 / recvErr 一次性注入错误 / 空则阻塞 ctx。
// 同 fakeOrderProfitStream 模式（mt_margin_source_test.go）。
type fakeQuoteStream struct {
	ctx     context.Context
	frames  chan *mt5pb.OnQuoteReply
	recvErr error // 一次性：无帧时返回此错（返回后视为已发，不再重复）
	errSent bool
	hdr, tr metadata.MD
}

func newFakeQuoteStream(ctx context.Context, frames chan *mt5pb.OnQuoteReply, recvErr error) *fakeQuoteStream {
	if frames == nil {
		frames = make(chan *mt5pb.OnQuoteReply, 4)
	}
	return &fakeQuoteStream{ctx: ctx, frames: frames, recvErr: recvErr}
}

func (s *fakeQuoteStream) Recv() (*mt5pb.OnQuoteReply, error) {
	// 优先消费已缓冲帧（channel close 后也会触发 case，返零值+false，需检测）。
	select {
	case f, ok := <-s.frames:
		if !ok {
			// frames channel 已 close → 流结束语义：返 recvErr（若有）否则 ctx 阻塞。
			break
		}
		return f, nil
	default:
	}
	if s.recvErr != nil && !s.errSent {
		s.errSent = true
		return nil, s.recvErr
	}
	select {
	case f, ok := <-s.frames:
		if !ok {
			// channel close 且无 recvErr → 阻塞于 ctx（模拟流断后等 ctx 取消）。
			break
		}
		return f, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
	// frames close 且无 err：阻塞 ctx（等取消或外部投递，本测试不触达此路径后继续）。
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

func (s *fakeQuoteStream) Header() (metadata.MD, error) { return s.hdr, nil }
func (s *fakeQuoteStream) Trailer() metadata.MD         { return s.tr }
func (s *fakeQuoteStream) CloseSend() error             { return nil }
func (s *fakeQuoteStream) Context() context.Context     { return s.ctx }
func (s *fakeQuoteStream) SendMsg(_ interface{}) error  { return nil }
func (s *fakeQuoteStream) RecvMsg(_ interface{}) error  { return nil }

// --- 测试辅助 ---

// withShortQuoteBackoff 临时缩短 marginReconnectBackoff（OnQuote 流复用此序列），返回恢复函数。
func withShortQuoteBackoff(t *testing.T, recon []time.Duration) func() {
	t.Helper()
	orig := marginReconnectBackoff
	marginReconnectBackoff = recon
	return func() { marginReconnectBackoff = orig }
}

// newQuoteTestAdapter 构造测试用 MT5Adapter（mt5/subs/streams 同一 fake，connMgr 可选）。
// 初始化 stopCtx（Subscribe 派生 ctx 依赖它，nil 会 panic）。
func newQuoteTestAdapter(client *fakeMT5Client, connMgr mt5pb.ConnectionClient) *MT5Adapter {
	ctx, cancel := context.WithCancel(context.Background())
	a := &MT5Adapter{venue: "vq", mt5: client, subs: client, streams: client, token: "tok", stopCtx: ctx, stopCancel: cancel}
	if connMgr != nil {
		a.connMgr = connMgr
	}
	return a
}

// startQuoteStream 启动 Subscribe 并返回 quote 通道 + cleanup。
// cleanup 取消 ctx 并等待 runQuoteStream goroutine 退出（stopCtx 取消 → ctx 取消 → defer close(ch)）。
// 不用 range ch 等待（阻塞流无帧时 range 永远不返回）；改用 stopCtx 派生 ctx 的 Done。
func startQuoteStream(t *testing.T, a *MT5Adapter, symbols []string) (<-chan bus.Quote, func()) {
	t.Helper()
	ch, cancel := a.Subscribe(symbols)
	return ch, func() {
		cancel()
		// 等 ch 关闭（runQuoteStream defer close(ch)），超时保护防挂死。
		select {
		case _, ok := <-ch:
			_ = ok
		case <-time.After(3 * time.Second):
			t.Errorf("ch not closed within 3s after cancel")
		}
	}
}

// --- 单测 ---

// TestQuoteStream_ReconnectAfterRecvErr 注入 1 帧 → Recv 返 Unavailable err →
// 重连退避 → 第 2 条流继续投递帧到 ch。验证流断后自动重开 + 帧恢复。
func TestQuoteStream_ReconnectAfterRecvErr(t *testing.T) {
	restore := withShortQuoteBackoff(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond})
	defer restore()

	client := &fakeMT5Client{}
	// 第 1 条流：1 帧后返 Unavailable（GOAWAY 模拟，IsSessionLost=true → 触发 reconnectSession）。
	frame1 := make(chan *mt5pb.OnQuoteReply, 2)
	frame1 <- &mt5pb.OnQuoteReply{Result: &mt5pb.Quote{Symbol: "AUDJPY", Bid: 113.5, Ask: 113.51}}
	close(frame1)
	stream1 := newFakeQuoteStream(context.Background(), frame1, status.Error(codes.Unavailable, "closing transport: EOF"))
	// 第 2 条流：1 帧后阻塞（验证重连后帧照常到）。
	frame2 := make(chan *mt5pb.OnQuoteReply, 2)
	stream2 := newFakeQuoteStream(context.Background(), frame2, nil)
	client.onQuoteStreams = []grpc.ServerStreamingClient[mt5pb.OnQuoteReply]{stream1, stream2}

	a := newQuoteTestAdapter(client, &fakeConnMgr{})
	a.SetSymbolMap(map[string]string{"AUDJPY": "AUDJPY"})
	ch, cleanup := startQuoteStream(t, a, []string{"AUDJPY"})
	defer cleanup()

	// 第 1 帧（第 1 条流）。
	q1 := <-ch
	if q1.Symbol != "AUDJPY" || q1.Bid != 113.5 {
		t.Fatalf("frame1: symbol=%q bid=%v, want AUDJPY/113.5", q1.Symbol, q1.Bid)
	}
	// 第 1 条流 Recv 返 err → 重连退避 → 第 2 条流开 → 投递 frame2。
	frame2 <- &mt5pb.OnQuoteReply{Result: &mt5pb.Quote{Symbol: "AUDJPY", Bid: 113.6, Ask: 113.61}}
	q2 := <-ch
	if q2.Bid != 113.6 {
		t.Fatalf("frame2: bid=%v, want 113.6 (reconnected stream)", q2.Bid)
	}
	if client.onQuoteCallsLoad() < 2 {
		t.Errorf("onQuoteCalls=%d, want >=2 (reconnect reopened stream)", client.onQuoteCallsLoad())
	}
}

// TestQuoteStream_ResubscribeSurvivesReconnect Subscribe([A]) → Resubscribe([B]) →
// 触发重连 → 断言 resubscribeAll 调用集 = {A,B}（fake subs.Subscribe 记录）。
func TestQuoteStream_ResubscribeSurvivesReconnect(t *testing.T) {
	restore := withShortQuoteBackoff(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond})
	defer restore()

	client := &fakeMT5Client{}
	// 第 1 条流：立即返 err 触发重连。
	stream1 := newFakeQuoteStream(context.Background(), nil, status.Error(codes.Unavailable, "GOAWAY"))
	// 第 2 条流：阻塞（仅验证重订阅，不消费帧）。
	stream2 := newFakeQuoteStream(context.Background(), nil, nil)
	client.onQuoteStreams = []grpc.ServerStreamingClient[mt5pb.OnQuoteReply]{stream1, stream2}

	a := newQuoteTestAdapter(client, &fakeConnMgr{})
	_, cleanup := startQuoteStream(t, a, []string{"AUDJPY"})
	defer cleanup()
	// 增补符号 B。
	a.Resubscribe([]string{"EURUSD"})

	// 等待重连完成（第 2 条流开 → resubscribeAll 调用）。
	if !waitFor(t, 2*time.Second, func() bool { return client.onQuoteCallsLoad() >= 2 }) {
		t.Fatalf("onQuoteCalls=%d, want >=2 (reconnect not happened)", client.onQuoteCallsLoad())
	}
	// 断言重订阅集 = {AUDJPY, EURUSD}（resubscribeAll 用全集重订阅）。
	client.subMu.Lock()
	got := make(map[string]bool, len(client.subscribedSymbols))
	for s := range client.subscribedSymbols {
		got[s] = true
	}
	client.subMu.Unlock()
	if !got["AUDJPY"] || !got["EURUSD"] {
		t.Errorf("subscribed set after reconnect = %v, want {AUDJPY,EURUSD}", got)
	}
}

// TestQuoteStream_CtxCancelClosesCh ctx 取消 → ch 关闭（_, ok := <-ch ok=false）。
func TestQuoteStream_CtxCancelClosesCh(t *testing.T) {
	restore := withShortQuoteBackoff(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond})
	defer restore()

	client := &fakeMT5Client{}
	// 阻塞流（无帧无错，阻塞于 ctx）。
	client.onQuoteStreams = []grpc.ServerStreamingClient[mt5pb.OnQuoteReply]{
		newFakeQuoteStream(context.Background(), nil, nil),
	}

	a := newQuoteTestAdapter(client, nil)
	ch, cleanup := startQuoteStream(t, a, []string{"AUDJPY"})
	defer cleanup()
	// 直接 cancel stopCtx（Subscribe 内部 ctx 派生自 stopCtx），验证 ch 关闭语义。
	a.stopCancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Errorf("ch not closed after ctx cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ch not closed within 2s after cancel")
	}
}

// TestQuoteStream_SessionLostTriggersReconnect Recv 返 "Value cannot be null" →
// 断言 connMgr.Connect 调用计数+1（reconnectSession 触发）。
func TestQuoteStream_SessionLostTriggersReconnect(t *testing.T) {
	restore := withShortQuoteBackoff(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond})
	defer restore()

	client := &fakeMT5Client{}
	connMgr := &fakeConnMgr{}
	// 第 1 条流：返 "Value cannot be null"（IsSessionLost 特征串）。
	stream1 := newFakeQuoteStream(context.Background(), nil, errors.New("rpc error: code = Internal desc = Value cannot be null"))
	// 第 2 条流：阻塞。
	stream2 := newFakeQuoteStream(context.Background(), nil, nil)
	client.onQuoteStreams = []grpc.ServerStreamingClient[mt5pb.OnQuoteReply]{stream1, stream2}

	a := newQuoteTestAdapter(client, connMgr)
	_, cleanup := startQuoteStream(t, a, []string{"AUDJPY"})
	defer cleanup()

	// 等待重连（connMgr.Connect 调用）。
	if !waitFor(t, 2*time.Second, func() bool { return connMgr.connectCallsLoad() >= 1 }) {
		t.Fatalf("connMgr.connectCalls=%d, want >=1 (session lost did not trigger reconnect)", connMgr.connectCallsLoad())
	}
	// 验证 onQuote 流也重开（重连后重开流）。
	if !waitFor(t, 2*time.Second, func() bool { return client.onQuoteCallsLoad() >= 2 }) {
		t.Errorf("onQuoteCalls=%d, want >=2 (stream not reopened after reconnect)", client.onQuoteCallsLoad())
	}
}
