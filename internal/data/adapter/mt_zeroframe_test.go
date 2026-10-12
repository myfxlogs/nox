// mt_zeroframe_test.go —— D-432（2026-09-24）：OnQuote 零帧死推检测告警单测（先红后绿）。
// 六用例：
//   - T1 HeartbeatsOnlyTriggersAlert：fake 流只吐 {} 心跳 → anyFrame 新鲜+realFrame 死
//     → 过阈值 stage-1 warn（AlertSink 断言一次不重复，跨 stage-2 重连仍去重）；
//   - T2 RealFramesNoAlert：真帧+心跳混合 → 不触发；
//   - T3 SilentStreamStage2（D-563 改义）：整流零帧（含心跳也无）>TTL → suspect 告警+
//     stage-2 强重连（D6 实证盲孔：lastAny 恒零原被整段跳过=静默死）；
//   - T4 Stage2SingleShot：死推持续 → 强制重连一次后不再自动重试 + persist warn；
//     真帧恢复 → episode 清零可再告警；
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

	"google.golang.org/grpc"
	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
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

// TestZeroframe_SilentStreamStage2 T3（D-563 改义）：整流零帧（阻塞流无一帧、
// lastAny 恒零）>suspectTTL → suspect 告警 + stage-2 取消流强重连（第 2 条流开）。
// 原义「静默=传输问题归 Recv 路径」被 D6 实证推翻：生产楔死正此形态（盲孔静默死）。
func TestZeroframe_SilentStreamStage2(t *testing.T) {
	defer withShortZeroFrame()()
	restore := withShortQuoteBackoff(t, []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond})
	defer restore()

	client := &fakeMT5Client{}
	client.onQuoteStreams = []grpc.ServerStreamingClient[mt5pb.OnQuoteReply]{
		newFakeQuoteStream(context.Background(), nil, nil),
		newFakeQuoteStream(context.Background(), nil, nil),
	}

	a := newQuoteTestAdapter(client, nil)
	rec := &alertRec{}
	a.AlertSink = rec.sink
	_, cleanup := startQuoteStream(t, a, []string{"XAUUSD"})
	defer cleanup()

	if !waitFor(t, 3*time.Second, func() bool { return rec.count("zeroframe suspect") >= 1 }) {
		t.Fatalf("零帧 suspect 告警未发; msgs=%v", rec.msgs)
	}
	if !waitFor(t, 3*time.Second, func() bool { return client.onQuoteCallsLoad() >= 2 }) {
		t.Fatalf("onQuoteCalls=%d, want >=2（stage-2 强重连）", client.onQuoteCallsLoad())
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
