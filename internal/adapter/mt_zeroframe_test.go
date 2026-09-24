// mt_zeroframe_test.go —— D-432（2026-09-24）：OnQuote 零帧死推检测告警单测（先红后绿）。
// 六用例：
//   - T1 HeartbeatsOnlyTriggersAlert：fake 流只吐 {} 心跳 → anyFrame 新鲜+realFrame 死
//     → 过阈值 stage-1 warn（AlertSink 断言一次不重复，跨 stage-2 重连仍去重）；
//   - T2 RealFramesNoAlert：真帧+心跳混合 → 不触发；
//   - T3 SilentStreamNoAlert：完全静默（无帧）→ 不触发（传输问题归 Recv 路径，不误报）；
//   - T4 Stage2SingleShot：死推持续 → 强制重连一次后不再自动重试 + persist warn；
//     真帧恢复 → episode 清零可再告警；
//   - T5 MT4同构锚：同 T1 逻辑 mt4pb 路径；
//   - T6 WatchdogExitsOnCancel：ctx 取消 → 看门狗退出不泄漏（ch 关闭时序保证）。
package adapter

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	mt4pb "arb/internal/adapter/gen/mtapi/mt4"
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// --- 测试辅助 ---

// withShortZeroFrame 临时缩短 D-432 看门狗参数（同 withShortQuoteBackoff 模式），返回恢复函数。
func withShortZeroFrame() func() {
	ci, hb, sus, rec := zeroFrameCheckInterval, zeroFrameHeartbeatTTL, zeroFrameSuspectTTL, zeroFrameRecoveryTTL
	zeroFrameCheckInterval = 10 * time.Millisecond
	zeroFrameHeartbeatTTL = 200 * time.Millisecond
	zeroFrameSuspectTTL = 60 * time.Millisecond
	zeroFrameRecoveryTTL = 80 * time.Millisecond
	return func() {
		zeroFrameCheckInterval, zeroFrameHeartbeatTTL, zeroFrameSuspectTTL, zeroFrameRecoveryTTL = ci, hb, sus, rec
	}
}

// alertRec AlertSink 捕获（并发安全，watchdog goroutine 调用）。
type alertRec struct {
	mu   sync.Mutex
	msgs []string
}

func (r *alertRec) sink(level, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, level+" "+msg)
}

func (r *alertRec) count(sub string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, m := range r.msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

// lockedBuf 并发安全 log 输出捕获（watchdog 异步 log，读时可能仍在写）。
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// feedMT5Frames 周期投递帧到 fake 流（满则丢，保 lastAny 新鲜即可）。
func feedMT5Frames(stop <-chan struct{}, frames chan<- *mt5pb.OnQuoteReply, mk func() *mt5pb.OnQuoteReply, period time.Duration) {
	go func() {
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				select {
				case frames <- mk():
				default:
				}
			}
		}
	}()
}

func heartbeat5() *mt5pb.OnQuoteReply { return &mt5pb.OnQuoteReply{} }
func realQuote5() *mt5pb.OnQuoteReply {
	return &mt5pb.OnQuoteReply{Result: &mt5pb.Quote{Symbol: "XAUUSD", Bid: 2000, Ask: 2000.1}}
}

// --- MT4 fake（mt4pb 路径同构锚） ---

// fakeMT4StreamClient 实现 MT4 报价流测试用到的 Subscriptions+Streams（同 fakeMT5Client 模式：
// 内嵌接口，未覆盖方法调用 panic）。a.subs/a.streams 可同赋一个实例。
type fakeMT4StreamClient struct {
	mt4pb.MT4Client
	mt4pb.SubscriptionsClient
	mt4pb.StreamsClient
	onQuoteMu      sync.Mutex
	onQuoteCalls   int
	onQuoteStreams []grpc.ServerStreamingClient[mt4pb.OnQuoteReply]
	onQuoteIdx     int
}

func (f *fakeMT4StreamClient) Subscribe(_ context.Context, _ *mt4pb.SubscribeRequest, _ ...grpc.CallOption) (*mt4pb.SubscribeReply, error) {
	return &mt4pb.SubscribeReply{}, nil
}

func (f *fakeMT4StreamClient) OnQuote(ctx context.Context, _ *mt4pb.OnQuoteRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[mt4pb.OnQuoteReply], error) {
	f.onQuoteMu.Lock()
	f.onQuoteCalls++
	var s grpc.ServerStreamingClient[mt4pb.OnQuoteReply]
	if f.onQuoteIdx < len(f.onQuoteStreams) {
		s = f.onQuoteStreams[f.onQuoteIdx]
		f.onQuoteIdx++
	}
	f.onQuoteMu.Unlock()
	if s != nil {
		if fs, ok := s.(*fakeQuoteStream4); ok {
			fs.ctx = ctx
		}
		return s, nil
	}
	return newFakeQuoteStream4(ctx, nil), nil
}

func (f *fakeMT4StreamClient) onQuoteCallsLoad() int {
	f.onQuoteMu.Lock()
	defer f.onQuoteMu.Unlock()
	return f.onQuoteCalls
}

// fakeQuoteStream4 MT4 版可控 OnQuote 流（同 fakeQuoteStream 模式）。
type fakeQuoteStream4 struct {
	ctx    context.Context
	frames chan *mt4pb.OnQuoteReply
	hdr    metadata.MD
	tr     metadata.MD
}

func newFakeQuoteStream4(ctx context.Context, frames chan *mt4pb.OnQuoteReply) *fakeQuoteStream4 {
	if frames == nil {
		frames = make(chan *mt4pb.OnQuoteReply, 4)
	}
	return &fakeQuoteStream4{ctx: ctx, frames: frames}
}

func (s *fakeQuoteStream4) Recv() (*mt4pb.OnQuoteReply, error) {
	select {
	case f, ok := <-s.frames:
		if ok {
			return f, nil
		}
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

func (s *fakeQuoteStream4) Header() (metadata.MD, error) { return s.hdr, nil }
func (s *fakeQuoteStream4) Trailer() metadata.MD         { return s.tr }
func (s *fakeQuoteStream4) CloseSend() error             { return nil }
func (s *fakeQuoteStream4) Context() context.Context     { return s.ctx }
func (s *fakeQuoteStream4) SendMsg(_ interface{}) error  { return nil }
func (s *fakeQuoteStream4) RecvMsg(_ interface{}) error  { return nil }

func feedMT4Frames(stop <-chan struct{}, frames chan<- *mt4pb.OnQuoteReply, mk func() *mt4pb.OnQuoteReply, period time.Duration) {
	go func() {
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				select {
				case frames <- mk():
				default:
				}
			}
		}
	}()
}

func newMT4QuoteTestAdapter(client *fakeMT4StreamClient) *MT4Adapter {
	ctx, cancel := context.WithCancel(context.Background())
	return &MT4Adapter{venue: "vq4", subs: client, streams: client, token: "tok", stopCtx: ctx, stopCancel: cancel}
}

// --- 单测 ---

// TestZeroframe_HeartbeatsOnlyTriggersAlert T1：只吐 {} 心跳 → 过阈值 stage-1 warn，
// 且 episode 去重（跨 stage-2 强制重连后仍只告警一次）。
func TestZeroframe_HeartbeatsOnlyTriggersAlert(t *testing.T) {
	defer withShortZeroFrame()()
	restore := withShortQuoteBackoff(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond})
	defer restore()

	client := &fakeMT5Client{}
	frames1 := make(chan *mt5pb.OnQuoteReply, 8)
	frames2 := make(chan *mt5pb.OnQuoteReply, 8)
	stream1 := newFakeQuoteStream(context.Background(), frames1, nil)
	stream2 := newFakeQuoteStream(context.Background(), frames2, nil)
	client.onQuoteStreams = []grpc.ServerStreamingClient[mt5pb.OnQuoteReply]{stream1, stream2}

	stop := make(chan struct{})
	defer close(stop)
	feedMT5Frames(stop, frames1, heartbeat5, 5*time.Millisecond)
	feedMT5Frames(stop, frames2, heartbeat5, 5*time.Millisecond)

	a := newQuoteTestAdapter(client, nil)
	rec := &alertRec{}
	a.AlertSink = rec.sink
	_, cleanup := startQuoteStream(t, a, []string{"XAUUSD"})
	defer cleanup()

	if !waitFor(t, 3*time.Second, func() bool { return rec.count("zeroframe suspect") >= 1 }) {
		t.Fatalf("stage-1 alert not fired; msgs=%v", rec.msgs)
	}
	// 等 stage-2 强制重连（第 2 条流开）+ 若干拍，告警仍应=1（episode 去重跨重连）。
	if !waitFor(t, 3*time.Second, func() bool { return client.onQuoteCallsLoad() >= 2 }) {
		t.Fatalf("onQuoteCalls=%d, want >=2 (stage-2 reopen not happened)", client.onQuoteCallsLoad())
	}
	time.Sleep(5 * zeroFrameCheckInterval)
	if n := rec.count("zeroframe suspect"); n != 1 {
		t.Errorf("alert count=%d, want 1（episode 去重）", n)
	}
}

// TestZeroframe_RealFramesNoAlert T2：真帧+心跳混合 → 不触发。
func TestZeroframe_RealFramesNoAlert(t *testing.T) {
	defer withShortZeroFrame()()
	restore := withShortQuoteBackoff(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond})
	defer restore()

	client := &fakeMT5Client{}
	frames := make(chan *mt5pb.OnQuoteReply, 8)
	client.onQuoteStreams = []grpc.ServerStreamingClient[mt5pb.OnQuoteReply]{
		newFakeQuoteStream(context.Background(), frames, nil),
	}

	stop := make(chan struct{})
	defer close(stop)
	feedMT5Frames(stop, frames, realQuote5, 5*time.Millisecond)

	a := newQuoteTestAdapter(client, nil)
	rec := &alertRec{}
	a.AlertSink = rec.sink
	ch, cleanup := startQuoteStream(t, a, []string{"XAUUSD"})
	defer cleanup()

	// 持续排空 ch（真实下游持续消费；不排空 consume 会阻塞在 ch 发送上=帧计数停更）。
	go func() {
		for range ch {
		}
	}()
	time.Sleep(5 * zeroFrameSuspectTTL)
	if n := rec.count("zeroframe suspect"); n != 0 {
		t.Errorf("alert count=%d, want 0（真帧在流）", n)
	}
}

// TestZeroframe_SilentStreamNoAlert T3：完全静默（无帧）→ 不触发（流静止归 Recv 路径）。
func TestZeroframe_SilentStreamNoAlert(t *testing.T) {
	defer withShortZeroFrame()()
	restore := withShortQuoteBackoff(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond})
	defer restore()

	client := &fakeMT5Client{}
	// 阻塞流（无帧无错，阻塞于 ctx）。
	client.onQuoteStreams = []grpc.ServerStreamingClient[mt5pb.OnQuoteReply]{
		newFakeQuoteStream(context.Background(), nil, nil),
	}

	a := newQuoteTestAdapter(client, nil)
	rec := &alertRec{}
	a.AlertSink = rec.sink
	_, cleanup := startQuoteStream(t, a, []string{"XAUUSD"})
	defer cleanup()

	time.Sleep(5 * zeroFrameSuspectTTL)
	if n := rec.count("zeroframe suspect"); n != 0 {
		t.Errorf("alert count=%d, want 0（流静止不误报）", n)
	}
}

// TestZeroframe_Stage2SingleShot T4：死推持续 → 强制重连一次后不再自动重试 + persist warn；
// 真帧恢复 → episode 清零可再告警。
func TestZeroframe_Stage2SingleShot(t *testing.T) {
	defer withShortZeroFrame()()
	restore := withShortQuoteBackoff(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond})
	defer restore()

	lbuf := &lockedBuf{}
	log.SetOutput(lbuf)
	defer log.SetOutput(os.Stderr)

	client := &fakeMT5Client{}
	frames1 := make(chan *mt5pb.OnQuoteReply, 8)
	frames2 := make(chan *mt5pb.OnQuoteReply, 8)
	frames3 := make(chan *mt5pb.OnQuoteReply, 8)
	client.onQuoteStreams = []grpc.ServerStreamingClient[mt5pb.OnQuoteReply]{
		newFakeQuoteStream(context.Background(), frames1, nil),
		newFakeQuoteStream(context.Background(), frames2, nil),
		newFakeQuoteStream(context.Background(), frames3, nil),
	}

	stop := make(chan struct{})
	defer close(stop)
	feedMT5Frames(stop, frames1, heartbeat5, 5*time.Millisecond)
	feedMT5Frames(stop, frames2, heartbeat5, 5*time.Millisecond)
	feedMT5Frames(stop, frames3, heartbeat5, 5*time.Millisecond)

	a := newQuoteTestAdapter(client, nil)
	rec := &alertRec{}
	a.AlertSink = rec.sink
	_, cleanup := startQuoteStream(t, a, []string{"XAUUSD"})
	defer cleanup()

	// stage-2 单发：强制重连一次（onQuoteCalls=2）后不再自动重试。
	if !waitFor(t, 3*time.Second, func() bool { return client.onQuoteCallsLoad() >= 2 }) {
		t.Fatalf("onQuoteCalls=%d, want 2 (stage-2 reopen)", client.onQuoteCallsLoad())
	}
	// 持续死推超过 recoveryTTL + 多拍，调用数钉在 2（无重连风暴）且 persist warn 落 log。
	time.Sleep(zeroFrameRecoveryTTL + 10*zeroFrameCheckInterval)
	if n := client.onQuoteCallsLoad(); n != 2 {
		t.Errorf("onQuoteCalls=%d, want 2（stage-2 单发不再重试）", n)
	}
	if !strings.Contains(lbuf.String(), "zeroframe persists after reconnect") {
		t.Errorf("missing persist warn log; got:\n%s", lbuf.String())
	}

	// 真帧恢复 → episode 清零（recovered 证据行 + 可再告警）。
	frames2 <- realQuote5()
	if !waitFor(t, 3*time.Second, func() bool {
		return strings.Contains(lbuf.String(), "zeroframe recovered after reconnect")
	}) {
		t.Fatalf("missing recovered log; got:\n%s", lbuf.String())
	}
	// 再持续死推 → episode 已清零 → 可再次 stage-1 告警 + stage-2 重连（第 3 条流）。
	if !waitFor(t, 3*time.Second, func() bool { return rec.count("zeroframe suspect") >= 2 }) {
		t.Fatalf("re-alert not fired after episode reset; msgs=%v", rec.msgs)
	}
	if !waitFor(t, 3*time.Second, func() bool { return client.onQuoteCallsLoad() >= 3 }) {
		t.Errorf("onQuoteCalls=%d, want 3（新 episode 重得 stage-2）", client.onQuoteCallsLoad())
	}
}

// TestZeroframe_MT4HeartbeatsOnly T5：MT4 同构锚（同 T1 逻辑 mt4pb 路径）。
func TestZeroframe_MT4HeartbeatsOnly(t *testing.T) {
	defer withShortZeroFrame()()
	restore := withShortQuoteBackoff(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond})
	defer restore()

	client := &fakeMT4StreamClient{}
	frames1 := make(chan *mt4pb.OnQuoteReply, 8)
	client.onQuoteStreams = []grpc.ServerStreamingClient[mt4pb.OnQuoteReply]{
		newFakeQuoteStream4(context.Background(), frames1),
		newFakeQuoteStream4(context.Background(), nil),
	}

	stop := make(chan struct{})
	defer close(stop)
	feedMT4Frames(stop, frames1, func() *mt4pb.OnQuoteReply { return &mt4pb.OnQuoteReply{} }, 5*time.Millisecond)

	a := newMT4QuoteTestAdapter(client)
	rec := &alertRec{}
	a.AlertSink = rec.sink
	ch, cancel := a.Subscribe([]string{"XAUUSD"})
	defer func() {
		cancel()
		select {
		case _, ok := <-ch:
			_ = ok
		case <-time.After(3 * time.Second):
			t.Errorf("mt4 ch not closed within 3s after cancel")
		}
	}()

	if !waitFor(t, 3*time.Second, func() bool { return rec.count("zeroframe suspect") >= 1 }) {
		t.Fatalf("mt4 stage-1 alert not fired; msgs=%v", rec.msgs)
	}
	if !strings.Contains(rec.msgs[0], "mt4:vq4") {
		t.Errorf("alert msg missing mt4 venue prefix; got %q", rec.msgs[0])
	}
}

// TestZeroframe_WatchdogExitsOnCancel T6：ctx 取消 → 看门狗退出不泄漏 goroutine。
// runQuoteStream 收口序=zfDone→close(ch)：ch 关闭即看门狗已退出（时序保证，非 goroutine 计数）。
func TestZeroframe_WatchdogExitsOnCancel(t *testing.T) {
	defer withShortZeroFrame()()
	restore := withShortQuoteBackoff(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond})
	defer restore()

	client := &fakeMT5Client{}
	frames := make(chan *mt5pb.OnQuoteReply, 8)
	client.onQuoteStreams = []grpc.ServerStreamingClient[mt5pb.OnQuoteReply]{
		newFakeQuoteStream(context.Background(), frames, nil),
	}

	stop := make(chan struct{})
	defer close(stop)
	feedMT5Frames(stop, frames, heartbeat5, 5*time.Millisecond)

	a := newQuoteTestAdapter(client, nil)
	ch, cleanup := startQuoteStream(t, a, []string{"XAUUSD"})
	// 等看门狗跑起来（至少一拍判定），再取消。
	time.Sleep(3 * zeroFrameCheckInterval)
	cleanup() // 内部断言 ch 3s 内关闭——ch 关闭=看门狗 zfDone 已收口（无泄漏）
	_ = ch
}
