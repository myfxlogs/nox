// binance_user_ws.go —— D-450 S5：Binance 用户数据流经 cex-proxy。
// listenKey 生命周期：POST {base}/v1/listenKey 取 key（base 已含 /fapi 段——
// DB 实拍 real=proxy/fapi、demo=proxy/demofapi/fapi；审计 R1 修订，勿再拼 /fapi）
// → wss 连接 → PUT 每 30min 续租（官方 60min 失效）→ 续租失败/读断 → 重新取 key
// 重连（key 不可复用跨重连）。事件 ORDER_TRADE_UPDATE / ACCOUNT_UPDATE。
// 订单/持仓频道无心跳——静默属正常态，存活判定=ping 20s+pong 顺延读超时 60s。
// spot 端点={base}/api/v3/userDataStream——生产实证 410 Gone（端点已下线，
// D-450 取证：双 /api 亦 410=端点死非路径错），装配层跳过 spot 行不启流。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"arb/internal/httpx"
	"arb/internal/safego"

	"github.com/gorilla/websocket"
)

const binanceUserReadIdle = 60 * time.Second

// BinanceUserWS Binance 用户数据流采集器（perp real/demo/spot 三路由穷举写死，不模式匹配猜）。
type BinanceUserWS struct {
	apiKey     string
	baseURL    string // exchange_configs.base_url
	venueType  string // perp / spot
	venueKey   string
	simulated  bool
	sink       VenueSnapshotSink
	events     PrivateEventSink
	orderCache *OrderEventCache
	posCache   *PositionEventCache

	rest       *http.Client
	dial       func(ctx context.Context, wsURL string) (*websocket.Conn, error)
	renewEvery time.Duration // 续租周期（默认 30min；测试注入缩短）
	now        func() time.Time
}

// NewBinanceUserWS 创建采集器。
func NewBinanceUserWS(apiKey, apiSecret, baseURL, venueType, venueKey string, simulated bool, sink VenueSnapshotSink, events PrivateEventSink, oc *OrderEventCache, pc *PositionEventCache) *BinanceUserWS {
	b := &BinanceUserWS{
		apiKey: apiKey, baseURL: baseURL, venueType: venueType, venueKey: venueKey,
		simulated: simulated, sink: sink, events: events,
		orderCache: oc, posCache: pc,
		rest: httpx.Client(), renewEvery: 30 * time.Minute, now: time.Now,
	}
	dialer, header := httpx.WSDialer()
	b.dial = func(ctx context.Context, wsURL string) (*websocket.Conn, error) {
		conn, _, err := dialer.DialContext(ctx, wsURL, header)
		return conn, err
	}
	return b
}

// listenKeyEndpoint REST 端点（审计 R1 修订版：perp=base+/v1/listenKey；spot=base+/api/v3/userDataStream）。
func (b *BinanceUserWS) listenKeyEndpoint() string {
	if b.venueType == "spot" {
		return b.baseURL + "/api/v3/userDataStream"
	}
	return b.baseURL + "/v1/listenKey"
}

// WSURL listenKey → WS 地址（三路穷举映射写死：real perp→/ws、demo perp→/demofws、spot→/spotws；
// baseURL 只取 scheme+host，路径段不继承——demofapi/fapi 的 REST 段不得进 WS URL）。
func (b *BinanceUserWS) WSURL(listenKey string) (string, error) {
	u, err := url.Parse(b.baseURL)
	if err != nil {
		return "", fmt.Errorf("parse baseURL: %w", err)
	}
	scheme := "wss"
	if u.Scheme == "http" {
		scheme = "ws"
	}
	var path string
	switch {
	case b.venueType == "perp" && !b.simulated:
		path = "/ws/" + listenKey
	case b.venueType == "perp" && b.simulated:
		path = "/demofws/ws/" + listenKey
	case b.venueType == "spot":
		path = "/spotws/ws/" + listenKey
	default:
		return "", fmt.Errorf("binance user ws: unsupported venueType=%q simulated=%v", b.venueType, b.simulated)
	}
	return fmt.Sprintf("%s://%s%s", scheme, u.Host, path), nil
}

// Run 重连循环（5s 退避）。每次迭代取新 listenKey——失效不复用（官方语义）。
func (b *BinanceUserWS) Run(ctx context.Context) {
	for {
		if err := b.runOnce(ctx); err != nil {
			log.Printf("[binance-user-ws] reconnect after error: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (b *BinanceUserWS) runOnce(ctx context.Context) error {
	key, err := b.obtainListenKey(ctx)
	if err != nil {
		return fmt.Errorf("listenKey: %w", err)
	}
	wsURL, err := b.WSURL(key)
	if err != nil {
		return err
	}
	conn, err := b.dial(ctx, wsURL)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	log.Printf("[binance-user-ws] connected (venue=%s:%s simulated=%v)", "binance", b.venueType, b.simulated)

	_ = conn.SetReadDeadline(b.now().Add(binanceUserReadIdle))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(b.now().Add(binanceUserReadIdle))
	})

	// 续租 goroutine：renewEvery 一次 PUT；失败 → 关连接触发 runOnce 返回重取 key。
	done := make(chan struct{})
	defer close(done)
	safego.Go("binance_user_ws:keepalive", func() {
		ticker := time.NewTicker(b.renewEvery)
		defer ticker.Stop()
		ping := time.NewTicker(20 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-ticker.C:
				if err := b.renewListenKey(ctx, key); err != nil {
					log.Printf("[binance-user-ws] renew listenKey failed: %v (reconnecting for fresh key)", err)
					_ = conn.Close()
					return
				}
			case <-ping.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	})

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		if b.handleFrame(ctx, data) {
			return fmt.Errorf("listenKeyExpired")
		}
	}
}

// keyReq listenKey REST 调用（X-MBX-APIKEY 头；httpx client 自带代理 token+CA）。
func (b *BinanceUserWS) keyReq(ctx context.Context, method, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-MBX-APIKEY", b.apiKey)
	resp, err := b.rest.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s: http %d: %s", method, endpoint, resp.StatusCode, truncateStr(string(data), 200))
	}
	return nil
}

func (b *BinanceUserWS) obtainListenKey(ctx context.Context) (string, error) {
	ep := b.listenKeyEndpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-MBX-APIKEY", b.apiKey)
	resp, err := b.rest.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("POST %s: http %d: %s", ep, resp.StatusCode, truncateStr(string(data), 200))
	}
	var r struct {
		ListenKey string `json:"listenKey"`
	}
	if err := json.Unmarshal(data, &r); err != nil || r.ListenKey == "" {
		return "", fmt.Errorf("listenKey response invalid: %s", truncateStr(string(data), 200))
	}
	return r.ListenKey, nil
}

// renewListenKey PUT 续租（fapi 同端点隐式续当前 key；spot 需 query listenKey=）。
func (b *BinanceUserWS) renewListenKey(ctx context.Context, key string) error {
	ep := b.listenKeyEndpoint()
	if b.venueType == "spot" {
		ep += "?listenKey=" + key
	}
	return b.keyReq(ctx, http.MethodPut, ep)
}
