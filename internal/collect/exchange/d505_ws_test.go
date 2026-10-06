// d505_ws_test.go —— D-507 保活收编红绿锚（编号沿用撞号后留痕：本票原占 D-505）。
// fake server 两变体（复用 d450 形态）：quiet=gorilla 默认自动回 pong 不推数据；
// dead=吞 ping 不回 pong 不推数据（SetPingHandler 返 nil 不写回）。
// 双向钉死：quiet 窗内零重连（pong 续期防误断）、dead 窗内判死重连（liveness 语义保）。
package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"

	"arb/internal/bus"
	"arb/internal/fact"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// d507Server 计数 upgrade 的静默 WS server；dead=true 吞 ping 不回 pong。
func d507Server(t *testing.T, dead bool) (url string, upgrades *atomic.Int64) {
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
		if dead {
			conn.SetPingHandler(func(string) error { return nil }) // 吞 ping 不回 pong
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil { // 只排空订阅帧，永不推数据
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), n
}

const d507Observe = 1500 * time.Millisecond

func d507GateWS() *GateWebSocket {
	g := NewGateWebSocket(Config{})
	g.readIdle = 300 * time.Millisecond
	g.pingEvery = 100 * time.Millisecond
	g.reconnectBackoff = 50 * time.Millisecond
	return g
}

// TestD507_GateQuietFeedStaysAlive 安静行情窗（服务端不推数据但回 pong）：
// 观察窗内 upgrade 必须==1——S0 态（逐读绝对读限）下必红（300ms 超时重连≥2）。
func TestD507_GateQuietFeedStaysAlive(t *testing.T) {
	url, n := d507Server(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := d507GateWS()
	go g.runLoop(ctx, []string{url}, []string{"spot.tickers"}, gateSpotWSURL, g.spotWL, g.handleSpot, func(q bus.Quote) {}, nil)
	time.Sleep(d507Observe)
	cancel()
	if got := n.Load(); got != 1 {
		t.Fatalf("quiet feed 被误断重连: upgrades=%d want 1（pong 应续期读限）", got)
	}
}

// TestD507_GateDeadPeerStillReconnects 死对端（吞 ping 无 pong 无数据）：
// 读限判死 → 重连，观察窗内 upgrade ≥2（liveness 语义保留）。
func TestD507_GateDeadPeerStillReconnects(t *testing.T) {
	url, n := d507Server(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := d507GateWS()
	go g.runLoop(ctx, []string{url}, []string{"spot.tickers"}, gateSpotWSURL, g.spotWL, g.handleSpot, func(q bus.Quote) {}, nil)
	time.Sleep(d507Observe)
	cancel()
	if got := n.Load(); got < 2 {
		t.Fatalf("dead peer 未判死重连: upgrades=%d want ≥2", got)
	}
}

// TestD507_OkxQuietFeedStaysAlive okx 同 quiet 形态（S0 同红→S1 绿）。
func TestD507_OkxQuietFeedStaysAlive(t *testing.T) {
	url, n := d507Server(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := NewOKXWebSocket(Config{})
	o.wsURLs = []string{url}
	o.readIdle = 300 * time.Millisecond
	o.pingEvery = 100 * time.Millisecond
	o.reconnectBackoff = 50 * time.Millisecond
	go o.Run(ctx, func(q bus.Quote) {})
	time.Sleep(d507Observe)
	cancel()
	if got := n.Load(); got != 1 {
		t.Fatalf("okx quiet feed 被误断重连: upgrades=%d want 1", got)
	}
}

// TestD507_NilAlertFnNoPanic AlertFn 缺省 + dead server 跑一轮无 panic（meter 静默跳过）。
func TestD507_NilAlertFnNoPanic(t *testing.T) {
	url, _ := d507Server(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := d507GateWS() // AlertFn 缺省 nil
	go g.runLoop(ctx, []string{url}, []string{"spot.tickers"}, gateSpotWSURL, g.spotWL, g.handleSpot, func(q bus.Quote) {}, nil)
	time.Sleep(d507Observe)
	cancel()
}

// TestD509_BinanceMarkPriceFunding markPriceUpdate→funding 三 fact（D-509 S2）。
func TestD509_BinanceMarkPriceFunding(t *testing.T) {
	var got []fact.Fact
	ws := &BinanceMarketWS{now: time.Now}
	ws.emitFacts = func(_ context.Context, fs []fact.Fact) error { got = append(got, fs...); return nil }
	// 实测帧形制（binance perp markPrice）：e/s/E/T/r/p/i/P
	ok := ws.handleMarkPrice(context.Background(), []byte(
		`{"e":"markPriceUpdate","E":1790467392,"s":"PAXGUSDT","p":"4000.1","i":"4000.2","P":"3999.9","r":"0.0001","T":1790496000000}`))
	if !ok {
		t.Fatal("markPriceUpdate 帧未消费")
	}
	if len(got) != 4 { // D-591 R2：帧含 i → +index_price 同帧落库
		t.Fatalf("facts=%d want 4（funding+next_settle+interval+index_price）", len(got))
	}
	if got[0].Kind != fact.KindFunding || got[0].Venue != "binance" || got[0].Symbol != "PAXGUSDT" ||
		got[0].Src != "binance_ws_markprice" {
		t.Fatalf("funding fact 键形错: %+v", got[0])
	}
	if f := got[3]; f.Kind != fact.KindIndexPrice || f.Value != 4000.2 || f.Unit != fact.UnitUSD {
		t.Fatalf("index fact want index_price=4000.2: %+v", f)
	}
	want := 0.0001 * 3 * 365 * 100 // 回退 3/天（intervals 未注入）
	if math.Abs(got[0].Value-want) > 1e-9 {
		t.Fatalf("annualized=%v want %v", got[0].Value, want)
	}
	if got[1].Kind != fact.KindFundingNextSettle || got[1].Value != 1790496000 {
		t.Fatalf("nextSettle: %+v", got[1])
	}
	if got[2].Kind != fact.KindFundingInterval || got[2].Value != 28800 {
		t.Fatalf("interval: %+v", got[2])
	}
	// 缺 r → 缺省不造
	got = nil
	if !ws.handleMarkPrice(context.Background(), []byte(`{"e":"markPriceUpdate","s":"BTCUSDT","T":1}`)) {
		t.Fatal("markPrice 帧应消费")
	}
	if len(got) != 0 {
		t.Fatalf("缺 rate 应缺省不造, got %d", len(got))
	}
	// 非 markPrice 帧 → 不消费
	if ws.handleMarkPrice(context.Background(), []byte(`{"s":"BTCUSDT","b":"1","a":"2"}`)) {
		t.Fatal("非 markPrice 帧不应消费")
	}
}

// TestD509_GateFuturesFunding gate futures.tickers 帧内 funding 字段→fact（D-509 S3；
// 帧样本=2026-09-27 fx-ws.gateio.ws 实测字段集）。
func TestD509_GateFuturesFunding(t *testing.T) {
	var got []fact.Fact
	var quotes []bus.Quote
	g := &GateWebSocket{}
	g.emitFacts = func(_ context.Context, fs []fact.Fact) error { got = append(got, fs...); return nil }
	g.handleFutures(context.Background(), []byte(`{"event":"update","result":[{
		"contract":"BTC_USDT","mark_price":"84334.6","t":1790467392692,
		"funding_rate":"-0.000014","funding_next_apply":1790496000,"funding_interval":28800}]}`),
		func(q bus.Quote) { quotes = append(quotes, q) })
	if len(quotes) != 1 || quotes[0].Symbol != "BTCUSDT" || quotes[0].Venue != "gate" {
		t.Fatalf("quote 面回归: %+v", quotes)
	}
	if len(got) != 2 {
		t.Fatalf("facts=%d want 2（funding+next_settle）", len(got))
	}
	if got[0].Kind != fact.KindFunding || got[0].Symbol != "BTCUSDT" || got[0].Src != "gate_ws_futures" {
		t.Fatalf("funding fact: %+v", got[0])
	}
	want := -0.000014 * 3 * 365 * 100 // interval=28800s→3/天（8h）
	if math.Abs(got[0].Value-want) > 1e-9 {
		t.Fatalf("annualized=%v want %v", got[0].Value, want)
	}
	if got[1].Kind != fact.KindFundingNextSettle || got[1].Value != 1790496000 {
		t.Fatalf("nextSettle: %+v", got[1])
	}
	// 缺 funding_rate → 不造 fact（行情 quote 照发）
	got = nil
	g.handleFutures(context.Background(), []byte(`{"event":"update","result":[{
		"contract":"ETH_USDT","mark_price":"3000","t":1790467392692}]}`),
		func(q bus.Quote) { quotes = append(quotes, q) })
	if len(got) != 0 {
		t.Fatalf("缺 rate 应缺省不造, got %d", len(got))
	}
}

// TestD509_OkxFundingRate funding-rate 频道帧→三 fact（D-509 S4）。
func TestD509_OkxFundingRate(t *testing.T) {
	var got []fact.Fact
	o := &OKXWebSocket{}
	o.emitFacts = func(_ context.Context, fs []fact.Fact) error { got = append(got, fs...); return nil }
	var msg wsMsg
	if err := json.Unmarshal([]byte(`{"arg":{"channel":"funding-rate","instId":"BTC-USDT-SWAP"},
		"data":[{"fundingRate":"0.0001","fundingTime":"1790467200000","nextFundingTime":"1790496000000"}]}`), &msg); err != nil {
		t.Fatal(err)
	}
	o.handleFundingRate(context.Background(), msg)
	if len(got) != 3 {
		t.Fatalf("facts=%d want 3", len(got))
	}
	if got[0].Kind != fact.KindFunding || got[0].Symbol != "BTCUSDT" || got[0].Src != "okx_ws_v5" {
		t.Fatalf("funding fact: %+v", got[0])
	}
	if math.Abs(got[0].Value-0.0001*3*365*100) > 1e-9 { // 8h 窗→3/天
		t.Fatalf("annualized=%v", got[0].Value)
	}
	// D-092：NextSettle=fundingTime（1790467200=真·下次结算）；nextFundingTime
	// （1790496000）是再下一期——旧断言存了后者=倒计时恒多一期。
	if got[1].Value != 1790467200 || got[2].Value != 28800 {
		t.Fatalf("settle/interval: %+v %+v", got[1], got[2])
	}
	// 缺 fundingRate → 缺省不造
	got = nil
	msg.Data[0] = map[string]any{"fundingTime": "1"}
	o.handleFundingRate(context.Background(), msg)
	if len(got) != 0 {
		t.Fatalf("缺 rate 应缺省不造, got %d", len(got))
	}
	// tickers 帧不被 funding 分流误吞：handleMessage 正常路
	got = nil
	o.emitFacts = func(_ context.Context, fs []fact.Fact) error { got = append(got, fs...); return nil }
}

// TestD092_OkxNextSettleFundingTime D-092 锚（先红后绿）：NextSettle fact 值=
// fundingTime（1791302400=真·下次结算），修复前发射 nextFundingTime=1791331200 必红；
// interval=nft−ft（28800s）回归不变。
func TestD092_OkxNextSettleFundingTime(t *testing.T) {
	var got []fact.Fact
	o := &OKXWebSocket{}
	o.emitFacts = func(_ context.Context, fs []fact.Fact) error { got = append(got, fs...); return nil }
	var msg wsMsg
	if err := json.Unmarshal([]byte(`{"arg":{"channel":"funding-rate","instId":"XAU-USDT-SWAP"},
		"data":[{"fundingRate":"0.0001","fundingTime":"1791302400000","nextFundingTime":"1791331200000"}]}`), &msg); err != nil {
		t.Fatal(err)
	}
	o.handleFundingRate(context.Background(), msg)
	if len(got) != 3 {
		t.Fatalf("facts=%d want 3", len(got))
	}
	if got[1].Kind != fact.KindFundingNextSettle || got[1].Value != 1791302400 {
		t.Fatalf("nextSettle 应=fundingTime 1791302400（修复前=nextFundingTime 1791331200 必红）: %+v", got[1])
	}
	if got[2].Kind != fact.KindFundingInterval || got[2].Value != 28800 {
		t.Fatalf("interval 回归: %+v", got[2])
	}
}

// TestD092_OkxFundingTimeStaleWarn D-033 自盯：fundingTime ≤ now（陈旧/越界）→
// 采样 warn 一行，venue 值照发不篡改。
func TestD092_OkxFundingTimeStaleWarn(t *testing.T) {
	var got []fact.Fact
	o := &OKXWebSocket{}
	o.emitFacts = func(_ context.Context, fs []fact.Fact) error { got = append(got, fs...); return nil }
	var msg wsMsg
	// ft=1790467200000（10-05 16:00Z，相对运行时恒为过去）→ ahead<0 → warn。
	if err := json.Unmarshal([]byte(`{"arg":{"channel":"funding-rate","instId":"XAU-USDT-SWAP"},
		"data":[{"fundingRate":"0.0001","fundingTime":"1790467200000","nextFundingTime":"1790496000000"}]}`), &msg); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	o.handleFundingRate(context.Background(), msg)
	if !strings.Contains(buf.String(), "fundingTime 越界") {
		t.Fatalf("陈旧 fundingTime 应触发自盯 warn, log=%q", buf.String())
	}
	if len(got) != 3 {
		t.Fatalf("warn 路径 venue 值照发不篡改: facts=%d want 3", len(got))
	}
}
