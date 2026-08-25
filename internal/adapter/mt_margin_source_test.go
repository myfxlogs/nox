// mt_margin_source_test.go —— D-099（2026-08-24）：margin 采集事件驱动化单测。
// 四用例（先红后绿：删 SwapFacts margin 段后旧用例编译红 → 重写后绿）：
//   - EventTriggersFetch：注入 1 帧事件 → sink 收到 init_margin_rate fact（Leverage=500 → 0.002）；
//   - Debounce：5min 内连续 3 帧 → sink 只收 1 次（AccountSummary 调用计数 1）；
//   - FirstPollRetry：首采 AccountSummary 先 err 后成功 → 重试后 sink 收到 fact；
//   - StreamReconnect：流先注入错误再重开 → 重订阅后事件帧照常触发。
//
// fakeMT5Client（mt_swap_poll_test.go）已内嵌 SubscriptionsClient + StreamsClient，
// 此处补 SubscribeOrderProfit / OnOrderProfit 覆盖 + fakeOrderProfitStream + fakeConnMgr。
package adapter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"arb/internal/fact"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// SubscribeOrderProfit 覆盖（D-099）：返回 OK（subsOPErr 时返回 err），计数供断言。
func (f *fakeMT5Client) SubscribeOrderProfit(_ context.Context, _ *mt5pb.SubscribeOrderProfitRequest, _ ...grpc.CallOption) (*mt5pb.SubscribeOrderProfitReply, error) {
	f.subsOPCalls++
	if f.subsOPErr != nil {
		return nil, f.subsOPErr
	}
	return &mt5pb.SubscribeOrderProfitReply{}, nil
}

// OnOrderProfit 覆盖（D-099）：按队列弹出预设流；onOPErr 一次性优先；队列空 → 阻塞流。
// 预设流创建时用 Background（测试 setup 早于 ctx），此处回填调用 ctx 使 Recv 在 ctx
// 取消时能解锁（否则 cleanup cancel 无法唤醒阻塞的 Recv → 测试挂死）。
func (f *fakeMT5Client) OnOrderProfit(ctx context.Context, _ *mt5pb.OnOrderProfitRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[mt5pb.OnOrderProfitReply], error) {
	f.onOPCalls++
	if f.onOPErr != nil {
		err := f.onOPErr
		f.onOPErr = nil // 一次性
		return nil, err
	}
	if f.onOPIdx < len(f.onOPStreams) {
		s := f.onOPStreams[f.onOPIdx]
		f.onOPIdx++
		if fs, ok := s.(*fakeOrderProfitStream); ok {
			fs.ctx = ctx // 回填调用 ctx（派生自测试 ctx），Recv 解锁依赖此
		}
		return s, nil
	}
	// 默认阻塞流（无帧无错，阻塞于 ctx）——仅关心首采的测试用。
	return newFakeOrderProfitStream(ctx, nil, nil), nil
}

// fakeOrderProfitStream 可控 OnOrderProfit 流：frames 缓冲帧 / recvErr 一次性注入错误 / 空则阻塞 ctx。
type fakeOrderProfitStream struct {
	ctx     context.Context
	frames  chan *mt5pb.OnOrderProfitReply
	recvErr error // 一次性：无帧时返回此错（返回后视为已发，不再重复）
	errSent bool
	hdr, tr metadata.MD
}

// newFakeOrderProfitStream 构造可控流；frames 为 nil 时建空缓冲通道。
func newFakeOrderProfitStream(ctx context.Context, frames chan *mt5pb.OnOrderProfitReply, recvErr error) *fakeOrderProfitStream {
	if frames == nil {
		frames = make(chan *mt5pb.OnOrderProfitReply, 4)
	}
	return &fakeOrderProfitStream{ctx: ctx, frames: frames, recvErr: recvErr}
}

func (s *fakeOrderProfitStream) Recv() (*mt5pb.OnOrderProfitReply, error) {
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

func (s *fakeOrderProfitStream) Header() (metadata.MD, error) { return s.hdr, nil }
func (s *fakeOrderProfitStream) Trailer() metadata.MD         { return s.tr }
func (s *fakeOrderProfitStream) CloseSend() error             { return nil }
func (s *fakeOrderProfitStream) Context() context.Context     { return s.ctx }
func (s *fakeOrderProfitStream) SendMsg(_ interface{}) error  { return nil }
func (s *fakeOrderProfitStream) RecvMsg(_ interface{}) error  { return nil }

// fakeConnMgr 模拟 connMgr.Connect（reconnectSession 用，D-099 流重连测试）。
// D-100：connectCalls 加 mutex 保护（reconnectSession goroutine 与 waitFor 断言并发读）。
type fakeConnMgr struct {
	mt5pb.ConnectionClient
	mu           sync.Mutex
	connectCalls int
	connectErr   error
	token        string
}

func (f *fakeConnMgr) Connect(_ context.Context, _ *mt5pb.ConnectRequest, _ ...grpc.CallOption) (*mt5pb.ConnectReply, error) {
	f.mu.Lock()
	f.connectCalls++
	f.mu.Unlock()
	if f.connectErr != nil {
		return nil, f.connectErr
	}
	tok := f.token
	if tok == "" {
		tok = "reconn-tok"
	}
	return &mt5pb.ConnectReply{Result: tok}, nil
}

// connectCallsLoad 原子读 connectCalls（waitFor 断言用，避免直接读触发 race）。
func (f *fakeConnMgr) connectCallsLoad() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connectCalls
}

// waitFor 轮询条件直到 timeout，返回是否满足（测试同步用，避免固定 sleep 的竞态）。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// withShortBackoff 临时缩短 margin 退避序列，返回恢复函数（测试快跑，默认值即生产值不变）。
func withShortBackoff(t *testing.T, first []time.Duration, recon []time.Duration, debounce time.Duration) func() {
	t.Helper()
	origFirst, origRecon, origDeb := marginFirstRetryBackoff, marginReconnectBackoff, marginDebounce
	marginFirstRetryBackoff, marginReconnectBackoff, marginDebounce = first, recon, debounce
	return func() {
		marginFirstRetryBackoff, marginReconnectBackoff, marginDebounce = origFirst, origRecon, origDeb
	}
}

// newMarginTestAdapter 构造测试用 MT5Adapter：mt5/subs/streams 同一 fake，connMgr/token 可选。
func newMarginTestAdapter(client *fakeMT5Client, connMgr mt5pb.ConnectionClient) *MT5Adapter {
	a := &MT5Adapter{venue: "v6", mt5: client, subs: client, streams: client, token: "tok"}
	if connMgr != nil {
		a.connMgr = connMgr
	}
	return a
}

// startMarginSource 启动 MarginFactSource.Run 并返回 sink 通道 + cleanup。
// cleanup 取消 ctx 并等待 Run goroutine 退出——必须 defer 调用，确保 goroutine 在测试
// 函数返回前完全退出（否则下一测试改写包级退避 var 会与残留 goroutine 的读竞争）。
func startMarginSource(t *testing.T, src *MarginFactSource) (chan fact.Fact, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	sinkCh := make(chan fact.Fact, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = src.Run(ctx, func(f fact.Fact) { sinkCh <- f })
	}()
	return sinkCh, func() {
		cancel()
		<-done
	}
}

// TestMarginSource_EventTriggersFetch 注入 1 帧事件 → sink 收到 init_margin_rate fact
// （Leverage=500 → 0.002，Symbol==venue）。首采强制失败（summaryFailN=4）使事件为唯一触发源。
func TestMarginSource_EventTriggersFetch(t *testing.T) {
	restore := withShortBackoff(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond}, marginReconnectBackoff, marginDebounce)
	defer restore()

	client := &fakeMT5Client{leverage: 500, summaryErr: errors.New("rpc unavailable"), summaryFailN: 4}
	a := newMarginTestAdapter(client, nil)

	frameCh := make(chan *mt5pb.OnOrderProfitReply, 4)
	frameCh <- &mt5pb.OnOrderProfitReply{Result: &mt5pb.ProfitUpdate{Margin: 1}}
	client.onOPStreams = []grpc.ServerStreamingClient[mt5pb.OnOrderProfitReply]{
		newFakeOrderProfitStream(context.Background(), frameCh, nil),
	}

	src := &MarginFactSource{a: a}
	sinkCh, cleanup := startMarginSource(t, src)
	defer cleanup()

	select {
	case f := <-sinkCh:
		if f.Kind != fact.KindInitMarginRate {
			t.Fatalf("kind = %q, want %q", f.Kind, fact.KindInitMarginRate)
		}
		if f.Value != 0.002 {
			t.Errorf("value = %v, want 0.002 (1/500)", f.Value)
		}
		if f.Symbol != "v6" || f.Venue != "v6" {
			t.Errorf("Symbol=%q Venue=%q, want v6/v6 (账户标识)", f.Symbol, f.Venue)
		}
		if f.Unit != fact.UnitRatio || f.Src != "mt5" {
			t.Errorf("Unit=%q Src=%q, want ratio/mt5", f.Unit, f.Src)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event-triggered fact not received within 2s")
	}
	// 首采 4 次失败 + 事件触发 1 次成功 = 5 次 AccountSummary。
	if client.summaryCalls != 5 {
		t.Errorf("AccountSummary calls = %d, want 5 (4 firstPoll fail + 1 event success)", client.summaryCalls)
	}
}

// TestMarginSource_Debounce 5min 内连续 3 帧 → sink 只收 1 次（AccountSummary 调用计数 1）。
// 首采成功（lastSuccess 设定）→ 3 帧全在防抖窗内 skip。
func TestMarginSource_Debounce(t *testing.T) {
	client := &fakeMT5Client{leverage: 500}
	a := newMarginTestAdapter(client, nil)

	frameCh := make(chan *mt5pb.OnOrderProfitReply, 4)
	client.onOPStreams = []grpc.ServerStreamingClient[mt5pb.OnOrderProfitReply]{
		newFakeOrderProfitStream(context.Background(), frameCh, nil),
	}

	src := &MarginFactSource{a: a}
	sinkCh, cleanup := startMarginSource(t, src)
	defer cleanup()

	// 首采成功 → sink 收 1（AccountSummary 调用 1）。
	select {
	case <-sinkCh:
	case <-time.After(2 * time.Second):
		t.Fatal("first poll fact not received within 2s")
	}
	// 注入 3 帧事件（全在 5min 防抖窗内 → skip）。
	for i := 0; i < 3; i++ {
		frameCh <- &mt5pb.OnOrderProfitReply{Result: &mt5pb.ProfitUpdate{}}
	}
	// 等待 3 帧被消费（frames 通道排空）+ debounce-skip 处理完。
	if !waitFor(t, 2*time.Second, func() bool { return len(frameCh) == 0 }) {
		t.Fatal("3 event frames not consumed within 2s")
	}
	time.Sleep(50 * time.Millisecond) // debounce-skip 同步处理余量
	// AccountSummary 仍只被首采调用 1 次（3 帧全 debounce skip）。
	if client.summaryCalls != 1 {
		t.Errorf("AccountSummary calls = %d, want 1 (3 frames debounced)", client.summaryCalls)
	}
	// sink 仍只有首采的 1 条（无新 fact 入通道）。
	select {
	case f := <-sinkCh:
		t.Errorf("debounce should skip all 3 frames, got extra fact: %+v", f)
	default:
	}
}

// TestMarginSource_FirstPollRetry 首采 AccountSummary 先 err 后成功 → 重试后 sink 收到 fact。
func TestMarginSource_FirstPollRetry(t *testing.T) {
	restore := withShortBackoff(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond}, marginReconnectBackoff, marginDebounce)
	defer restore()

	client := &fakeMT5Client{leverage: 500, summaryErr: errors.New("rpc unavailable"), summaryFailN: 1}
	a := newMarginTestAdapter(client, nil)

	src := &MarginFactSource{a: a}
	sinkCh, cleanup := startMarginSource(t, src)
	defer cleanup()

	select {
	case f := <-sinkCh:
		if f.Value != 0.002 {
			t.Errorf("value = %v, want 0.002 (1/500 after retry)", f.Value)
		}
		if f.Symbol != "v6" {
			t.Errorf("Symbol = %q, want v6", f.Symbol)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first-poll retry fact not received within 2s")
	}
	// 首采：第 1 次失败 + 第 2 次成功 = 2 次 AccountSummary。
	if client.summaryCalls != 2 {
		t.Errorf("AccountSummary calls = %d, want 2 (1 fail + 1 retry success)", client.summaryCalls)
	}
}

// TestMarginSource_StreamReconnect 流先注入错误再重开 → 重订阅后事件帧照常触发。
// 首采强制失败（summaryFailN=4）使事件为唯一触发源；stream1 Recv 返回 Unavailable（IsSessionLost=true）
// → reconnectSession（fakeConnMgr）→ 退避 → 重订阅 → stream2 帧触发 fetch。
func TestMarginSource_StreamReconnect(t *testing.T) {
	restore := withShortBackoff(t,
		[]time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond},
		[]time.Duration{10 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond},
		marginDebounce)
	defer restore()

	client := &fakeMT5Client{leverage: 500, summaryErr: errors.New("rpc unavailable"), summaryFailN: 4}
	connMgr := &fakeConnMgr{}
	a := newMarginTestAdapter(client, connMgr)

	// stream1：Recv 立即返回 Unavailable（会话失效）。
	stream1 := newFakeOrderProfitStream(context.Background(), nil, status.Error(codes.Unavailable, "rst stream"))
	// stream2：1 帧后阻塞。
	frameCh := make(chan *mt5pb.OnOrderProfitReply, 4)
	frameCh <- &mt5pb.OnOrderProfitReply{Result: &mt5pb.ProfitUpdate{}}
	stream2 := newFakeOrderProfitStream(context.Background(), frameCh, nil)
	client.onOPStreams = []grpc.ServerStreamingClient[mt5pb.OnOrderProfitReply]{stream1, stream2}

	src := &MarginFactSource{a: a}
	sinkCh, cleanup := startMarginSource(t, src)
	defer cleanup()

	select {
	case f := <-sinkCh:
		if f.Value != 0.002 {
			t.Errorf("value = %v, want 0.002 (post-reconnect event fetch)", f.Value)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("post-reconnect fact not received within 3s")
	}
	// 重订阅 2 次（stream1 断后重开 stream2）。
	if client.subsOPCalls != 2 {
		t.Errorf("SubscribeOrderProfit calls = %d, want 2 (initial + reconnect)", client.subsOPCalls)
	}
	// OnOrderProfit 开流 2 次。
	if client.onOPCalls != 2 {
		t.Errorf("OnOrderProfit calls = %d, want 2 (stream1 err + stream2 ok)", client.onOPCalls)
	}
	// reconnectSession 调用 connMgr.Connect 1 次（D-100：用 Load 避免 race）。
	if connMgr.connectCallsLoad() != 1 {
		t.Errorf("connMgr.Connect calls = %d, want 1 (session lost → reconnect)", connMgr.connectCallsLoad())
	}
	// 首采 4 次失败 + 事件触发 1 次成功 = 5 次 AccountSummary。
	if client.summaryCalls != 5 {
		t.Errorf("AccountSummary calls = %d, want 5 (4 firstPoll fail + 1 post-reconnect success)", client.summaryCalls)
	}
}

// 编译期断言：fakeOrderProfitStream 实现 grpc.ServerStreamingClient[mt5pb.OnOrderProfitReply]。
var _ grpc.ServerStreamingClient[mt5pb.OnOrderProfitReply] = (*fakeOrderProfitStream)(nil)
