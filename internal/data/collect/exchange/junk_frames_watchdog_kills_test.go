// d154_business_frames_test.go —— D-154：行情 WS 看门狗业务帧化锚测。
// ①杂帧流+零业务帧→dataIdle 判死重连+dataKil 归因冷却；②杂帧+业务帧混流不误杀；
// ③okx 帧分类全型覆盖（tickers→true、ack/error/instruments/funding-rate/零价→false）。
package exchange

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/data/bus"

	"github.com/gorilla/websocket"
)

// d154PushServer fake server：连接后按 interval 推播固定帧集（回 pong 保活——
// 控制面活/业务面死形态，正钉 D-154 病灶）。
func d154PushServer(t *testing.T, frames [][]byte, every time.Duration) (string, *atomic.Int64) {
	t.Helper()
	n := &atomic.Int64{}
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		n.Add(1)
		defer conn.Close()
		tick := time.NewTicker(every)
		defer tick.Stop()
		done := make(chan struct{})
		defer close(done)
		go func() { // 读泵防客户端侧 ping 帧堵缓冲
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
		for {
			select {
			case <-tick.C:
				for _, f := range frames {
					if conn.WriteMessage(websocket.TextMessage, f) != nil {
						return
					}
				}
			case <-done:
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), n
}

// TestD154_JunkFramesWatchdogKills 杂帧流续命遮蔽根治：对端持续发非业务帧
// （OnFrame 恒 false），dataIdle 窗内必杀重连。
func TestD154_JunkFramesWatchdogKills(t *testing.T) {
	url, upgrades := d154PushServer(t, [][]byte{
		[]byte(`{"arg":{"channel":"funding-rate"},"data":[{"fundingRate":"0.01"}]}`),
		[]byte(`{"event":"channel-conn-count","arg":{"channel":"tickers"}}`),
	}, 30*time.Millisecond)
	spec := wsRunnerSpec(url, make(chan []byte, 8))
	spec.OnFrame = func(d []byte) bool { return false } // 杂帧恒非业务
	spec.ReadIdle = 2 * time.Second                     // pong 续期保活——读限不参与
	spec.PingEvery = 50 * time.Millisecond
	spec.DataIdle = 300 * time.Millisecond
	spec.ReconnectBackoff = 30 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunWS(ctx, spec)
	deadline := time.Now().Add(3 * time.Second)
	for upgrades.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := upgrades.Load(); got < 2 {
		t.Fatalf("杂帧流遮 dataIdle 未判死重连: upgrades=%d want ≥2", got)
	}
}

// TestD154_JunkKillCooldownAttribution dataKil 归因：连续业务帧缺席杀记当前
// 候选冷却（CooldownKills 达阈后主候选被跳过，兜底端接管拨号）。
func TestD154_JunkKillCooldownAttribution(t *testing.T) {
	junk := [][]byte{[]byte(`{"event":"channel-conn-count"}`)}
	primURL, primUp := d154PushServer(t, junk, 30*time.Millisecond)
	backURL, backUp := d154PushServer(t, junk, 30*time.Millisecond)
	spec := wsRunnerSpec(primURL, make(chan []byte, 8))
	spec.Candidates = []WSCandidate{{URL: primURL}, {URL: backURL}}
	spec.OnFrame = func(d []byte) bool { return false }
	spec.ReadIdle = 2 * time.Second
	spec.PingEvery = 50 * time.Millisecond
	spec.DataIdle = 250 * time.Millisecond
	spec.ReconnectBackoff = 30 * time.Millisecond
	spec.CooldownKills = 1                 // 一次数据杀即冷却主端
	spec.CooldownWindow = 10 * time.Second // 观察窗内不回主
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunWS(ctx, spec)
	deadline := time.Now().Add(4 * time.Second)
	for backUp.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if backUp.Load() == 0 {
		t.Fatalf("主端僵尸杀未归因冷却切兜底: backUp=%d primUp=%d", backUp.Load(), primUp.Load())
	}
	// 注：全候选均冷却时 dialOrder 忽略冷却回拨主端（不掐死数据面=钦定语义），
	// 故不断言主端拨号上界——backup 在主端冷却期内被接入即证 dataKil 归因。
}

// TestD154_MixedFramesStayAlive 杂帧+业务帧混流不误杀（业务帧续期语义）。
func TestD154_MixedFramesStayAlive(t *testing.T) {
	url, upgrades := d154PushServer(t, [][]byte{
		[]byte(`{"event":"channel-conn-count"}`),
		[]byte(`{"biz":true}`),
	}, 40*time.Millisecond)
	spec := wsRunnerSpec(url, make(chan []byte, 8))
	spec.OnFrame = func(d []byte) bool { return strings.Contains(string(d), `"biz"`) }
	spec.ReadIdle = 2 * time.Second
	spec.PingEvery = 50 * time.Millisecond
	spec.DataIdle = 300 * time.Millisecond
	spec.ReconnectBackoff = 30 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunWS(ctx, spec)
	time.Sleep(1200 * time.Millisecond)
	if got := upgrades.Load(); got != 1 {
		t.Fatalf("混流被误杀重连: upgrades=%d want 1", got)
	}
}

// TestD154_OkxFrameClassification okx 帧分类全型覆盖（票面验收③）。
func TestD154_OkxFrameClassification(t *testing.T) {
	o := NewOKXWebSocket(Config{})
	var pubs int
	pub := func(bus.Quote) { pubs++ }
	ctx := context.Background()
	cases := []struct {
		name  string
		frame string
		want  bool
	}{
		{"tickers 产出", `{"arg":{"channel":"tickers","instId":"BTC-USDT"},"data":[{"bidPx":"100","askPx":"101","ts":"1700000000000"}]}`, true},
		{"tickers 零价", `{"arg":{"channel":"tickers","instId":"BTC-USDT"},"data":[{"bidPx":"0","askPx":"0","ts":"1700000000000"}]}`, false},
		{"tickers 空data", `{"arg":{"channel":"tickers","instId":"BTC-USDT"},"data":[]}`, false},
		{"funding-rate 不入集", `{"arg":{"channel":"funding-rate","instId":"BTC-USDT-SWAP"},"data":[{"fundingRate":"0.01"}]}`, false},
		{"channel-conn-count", `{"event":"channel-conn-count","arg":{"channel":"tickers","instId":"BTC-USDT"},"connCount":"1"}`, false},
		{"subscribe ack", `{"event":"subscribe","arg":{"channel":"tickers","instId":"BTC-USDT"},"connId":"x"}`, false},
		{"error 帧", `{"event":"error","code":"60012","msg":"bad"}`, false},
		{"instruments", `{"arg":{"channel":"instruments","instType":"SWAP"},"data":[{"instId":"BTC-USDT-SWAP","instType":"SWAP","state":"live"}]}`, false},
		{"解析失败", `not-json`, false},
	}
	for _, c := range cases {
		if got := o.handleMessage(ctx, []byte(c.frame), pub); got != c.want {
			t.Fatalf("%s: got=%v want=%v", c.name, got, c.want)
		}
	}
	if pubs != 1 {
		t.Fatalf("publish 次数=%d want 1", pubs)
	}
}
