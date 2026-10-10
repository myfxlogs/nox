// d166_probe_test.go —— D-166 私有流控制面探针锚。
// 锚：OKX per-channel pending→ack 键分（orders SWAP/SPOT 各证）、探针周期重订阅+
// ack 超时疑死→主动断连、gate 周期重连探针+per-channel 静默 warn（静≠死）。
package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestD166_OKXProbeChannels 探针键集=订阅 args 全量、instType 分键（orders SWAP/SPOT 双证）。
func TestD166_OKXProbeChannels(t *testing.T) {
	got := okxProbeChannels()
	want := map[string]bool{
		"account": true, "balance_and_position": true,
		"positions:SWAP": true, "orders:SWAP": true, "orders:SPOT": true,
		"liquidation-warning:ANY": true,
	}
	if len(got) != len(want) {
		t.Fatalf("channels=%v, want %d keys", got, len(want))
	}
	for _, k := range got {
		if !want[k] {
			t.Fatalf("unexpected key %q in %v", k, got)
		}
	}
	if got[3] == got[4] { // orders SWAP/SPOT 不得同键
		t.Fatalf("orders instType 键未分: %v", got)
	}
}

// TestD166_OKXAckKey ack 帧键分：instType 缺席退频道名，在场分键。
func TestD166_OKXAckKey(t *testing.T) {
	if okxAckKey("orders", "SWAP") == okxAckKey("orders", "SPOT") {
		t.Fatal("orders SWAP/SPOT ack 键未分")
	}
	if okxAckKey("account", "") != "account" {
		t.Fatal("裸频道键漂移")
	}
}

// TestD166_SubProbePendingAck pending→ack 状态机：sent 记账、ack 清对应频道、
// 超 bound 未达列疑死、健康 ack 不误报。
func TestD166_SubProbePendingAck(t *testing.T) {
	now := time.Unix(1700000000, 0)
	p := newSubProbe()
	p.sent([]string{"account", "orders:SWAP"}, now)
	if dead := p.overdue(now.Add(time.Second), 120*time.Second); len(dead) != 0 {
		t.Fatalf("未超界误报: %v", dead)
	}
	p.ack("orders:SWAP")
	p.ack("orders:SPOT") // 无关键——不得清 orders:SWAP 残留语义（已清）不动 account
	if dead := p.overdue(now.Add(121*time.Second), 120*time.Second); len(dead) != 1 || dead[0] != "account" {
		t.Fatalf("overdue=%v, want [account]", dead)
	}
	p.ack("account")
	if dead := p.overdue(now.Add(121*time.Second), 120*time.Second); len(dead) != 0 {
		t.Fatalf("健康 ack 后仍报: %v", dead)
	}
}

// TestD166_ChanSilenceDue per-channel 静默 warn：touch 后超 idle 报一次（冷却抑重报）、
// 未见帧频道不报（静≠死，连出生帧都没见过不证死）。
func TestD166_ChanSilenceDue(t *testing.T) {
	now := time.Unix(1700000000, 0)
	c := newChanSilence()
	c.touch("futures.balances", now)
	chs := []string{"futures.balances", "futures.orders"}
	if got := c.due(chs, now.Add(time.Hour), 24*time.Hour, 24*time.Hour); len(got) != 0 {
		t.Fatalf("未超 idle 误报: %v", got)
	}
	got := c.due(chs, now.Add(25*time.Hour), 24*time.Hour, 24*time.Hour)
	if len(got) != 1 || got[0] != "futures.balances" {
		t.Fatalf("due=%v, want [futures.balances]（未见帧频道跳过）", got)
	}
	if got := c.due(chs, now.Add(26*time.Hour), 24*time.Hour, 24*time.Hour); len(got) != 0 {
		t.Fatalf("冷却期内重报: %v", got)
	}
}

// TestD166_OKXProbeResubscribe 探针周期=发 op:subscribe 全量帧（同连接重订阅幂等径）。
func TestD166_OKXProbeResubscribe(t *testing.T) {
	old := okxPrivateProbeInterval
	okxPrivateProbeInterval = 50 * time.Millisecond
	defer func() { okxPrivateProbeInterval = old }()

	wsSrv := newWSFakeServer(t)
	o := NewOKXPrivateWS("K", "S", "P", false, nil, "okx:unified", nil, NewOrderEventCache(), NewPositionEventCache(), OKXPrivateHooks{})
	o.wsURLs = []string{wsURLFromHTTP(wsSrv.URL)}
	o.dialer = &websocket.Dialer{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go o.runOnce(ctx)

	readOp := func(op string) map[string]any {
		t.Helper()
		for {
			select {
			case m := <-wsSrv.got:
				if m["op"] == op {
					return m
				}
			case <-ctx.Done():
				t.Fatalf("no %s frame", op)
			}
		}
	}
	readOp("login")
	wsSrv.pushC <- json.RawMessage(`{"event":"login","arg":{}}`)
	readOp("subscribe") // 初次订阅
	// ack 首轮全频道（含 instType 键）
	for _, a := range okxPrivateSubscribeArgs() {
		arg, _ := json.Marshal(a)
		wsSrv.pushC <- json.RawMessage(`{"event":"subscribe","arg":` + string(arg) + `}`)
	}
	readOp("subscribe") // 探针周期重订阅=第二帧（50ms 内到）——同连接重订阅实证
}

// TestD166_OKXProbeAckTimeout 探针 ack 超界未达→主动断连（runOnce 返 err 供 Run 重连）。
func TestD166_OKXProbeAckTimeout(t *testing.T) {
	oldI, oldT, oldC := okxPrivateProbeInterval, okxProbeAckTimeout, okxProbeCheckEvery
	okxPrivateProbeInterval, okxProbeAckTimeout, okxProbeCheckEvery = 200*time.Millisecond, 80*time.Millisecond, 20*time.Millisecond
	defer func() {
		okxPrivateProbeInterval, okxProbeAckTimeout, okxProbeCheckEvery = oldI, oldT, oldC
	}()

	wsSrv := newWSFakeServer(t)
	o := NewOKXPrivateWS("K", "S", "P", false, nil, "okx:unified", nil, NewOrderEventCache(), NewPositionEventCache(), OKXPrivateHooks{})
	o.wsURLs = []string{wsURLFromHTTP(wsSrv.URL)}
	o.dialer = &websocket.Dialer{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- o.runOnce(ctx) }()

	// 过 login+首轮订阅；探针重订阅帧到后**不回 ack** → 超 80ms 疑死 → conn.Close → runOnce 返 err。
	sawSub := false
	deadline := time.After(500 * time.Millisecond)
	for !sawSub {
		select {
		case m := <-wsSrv.got:
			if m["op"] == "login" {
				wsSrv.pushC <- json.RawMessage(`{"event":"login","arg":{}}`)
			}
			if m["op"] == "subscribe" {
				if sawSub { // 第二帧=探针——不 ack
					continue
				}
				sawSub = true
				for _, a := range okxPrivateSubscribeArgs() {
					arg, _ := json.Marshal(a)
					wsSrv.pushC <- json.RawMessage(`{"event":"subscribe","arg":` + string(arg) + `}`)
				}
			}
		case <-deadline:
			t.Fatal("首轮订阅帧未到")
		}
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ack 超界未触发断连（runOnce nil return）")
		}
	case <-ctx.Done():
		t.Fatal("ack 超界未在 3s 内判死")
	}
}

// TestD166_GateProbeReconnect gate 周期重连探针：T 到 conn.Close→runOnce 返（Run 径重订阅）。
func TestD166_GateProbeReconnect(t *testing.T) {
	old := gatePrivateProbeInterval
	gatePrivateProbeInterval = 60 * time.Millisecond
	defer func() { gatePrivateProbeInterval = old }()

	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/futures/usdt/accounts") {
			w.Write([]byte(`{"user":42,"total":"100"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer rest.Close()
	wsSrv := newWSFakeServer(t)
	g := NewGatePrivateWS("K", "S", rest.URL+"/api/v4", "gate:perp", false, nil, nil, NewOrderEventCache(), NewPositionEventCache())
	g.rest = rest.Client()
	g.dial = func(ctx context.Context, wsURL string) (*websocket.Conn, error) {
		d := websocket.Dialer{}
		conn, _, err := d.DialContext(ctx, wsURLFromHTTP(wsSrv.URL), nil)
		return conn, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- g.runOnce(ctx) }()
	select {
	case <-done:
		// 探针到点主动断连→runOnce 返回（err 或 nil 皆可——read EOF 或 ctx 竞速）
	case <-ctx.Done():
		t.Fatal("探针未在 3s 内触发重连")
	}
}
