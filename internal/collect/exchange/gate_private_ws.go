// gate_private_ws.go —— D-450 S2：Gate futures 私有 WS 经 cex-proxy。
// 订阅 futures.balances/positions/orders/usertrades（payload 按频道分派：balances=[uid]、其余=[uid,"!all"]，
// uid 由 REST GET /futures/usdt/accounts 取回 "user" 字段）。签名：
// sign=hex(HMAC-SHA512(secret,"channel=<ch>&event=subscribe&time=<unix秒>"))。
// 存活判定=ping 20s+pong 顺延读超时 60s+error 事件 fail-loud（订单/持仓频道无心跳，
// 静默属正常态——禁用「无事件=死」判据，REST 轮询双轨兜底）。凭据零日志。
package exchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arb/internal/exec"
	"arb/internal/httpx"
	"arb/internal/safego"

	"github.com/gorilla/websocket"
)

const gatePrivateReadIdle = 60 * time.Second

// gatePrivateChannels D-450 订阅面（payload 形制见 subscribeFrame 按频道分派）。
var gatePrivateChannels = []string{
	"futures.balances",
	"futures.positions",
	"futures.orders",
	"futures.usertrades",
}

// GatePrivateWS Gate futures 私有流采集器。
type GatePrivateWS struct {
	apiKey     string
	apiSecret  string
	baseURL    string // exchange_configs.base_url（形如 https://proxy:8443/api/v4）
	venueKey   string // 撞键归一键（写缓存的 venueKey 身份）
	simulated  bool
	sink       VenueSnapshotSink
	events     PrivateEventSink
	orderCache *OrderEventCache
	posCache   *PositionEventCache

	dial   func(ctx context.Context, wsURL string) (*websocket.Conn, error) // 测试注入
	rest   *http.Client                                                     // uid/quanto 查询（测试注入）
	now    func() time.Time
	quanto map[string]float64 // contract→quanto_multiplier（executor 同款 lazy 缓存）
}

// NewGatePrivateWS 创建采集器。baseURL 空→调用方跳过（不落默认直连：key 白名单语义要求显式走代理）。
func NewGatePrivateWS(apiKey, apiSecret, baseURL, venueKey string, simulated bool, sink VenueSnapshotSink, events PrivateEventSink, oc *OrderEventCache, pc *PositionEventCache) *GatePrivateWS {
	g := &GatePrivateWS{
		apiKey: apiKey, apiSecret: apiSecret, baseURL: baseURL, venueKey: venueKey,
		simulated: simulated, sink: sink, events: events,
		orderCache: oc, posCache: pc,
		rest: httpx.Client(), now: time.Now,
		quanto: map[string]float64{},
	}
	dialer, header := httpx.WSDialer()
	g.dial = func(ctx context.Context, wsURL string) (*websocket.Conn, error) {
		conn, _, err := dialer.DialContext(ctx, wsURL, header)
		return conn, err
	}
	return g
}

// WSURL base_url → WS 端点派生（SSOT：TrimSuffix "/api/v4" + "/v4/ws/usdt"；https→wss/http→ws）。
func (g *GatePrivateWS) WSURL() string {
	u := strings.TrimSuffix(g.baseURL, "/api/v4") + "/v4/ws/usdt"
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	return u
}

// Run 重连循环（5s 退避，OKX 同款）。阻塞至 ctx 取消。
func (g *GatePrivateWS) Run(ctx context.Context) {
	for {
		if err := g.runOnce(ctx); err != nil {
			log.Printf("[gate-ws] reconnect after error: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (g *GatePrivateWS) runOnce(ctx context.Context) error {
	uid, err := g.fetchUID(ctx)
	if err != nil {
		return fmt.Errorf("fetch uid: %w", err)
	}
	conn, err := g.dial(ctx, g.WSURL())
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(g.now().Add(gatePrivateReadIdle))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(g.now().Add(gatePrivateReadIdle))
	})
	done := make(chan struct{})
	defer close(done)
	safego.Go("gate_private_ws:ping", func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	})

	// 逐频道签名订阅（每频道独立 auth 帧——签名含 channel 名）。
	for _, ch := range gatePrivateChannels {
		if err := conn.WriteJSON(g.subscribeFrame(ch, uid)); err != nil {
			return fmt.Errorf("subscribe %s: %w", ch, err)
		}
	}

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
		if err := g.handleFrame(ctx, data); err != nil {
			return err
		}
	}
}

// subscribeFrame 组装带签名的订阅帧（签名串不含 payload）。
// payload 形制按频道分派（生产实证矩阵 8 发全表：balances 双参报 "need 1 user id"，
// positions/orders/usertrades 单参报 "need market param"）：
// balances→[uid]，其余→[uid,"!all"]。
func (g *GatePrivateWS) subscribeFrame(channel, uid string) map[string]any {
	payload := []string{uid, "!all"}
	if channel == "futures.balances" {
		payload = []string{uid}
	}
	ts := g.now().Unix()
	sign := gateWSSign(g.apiSecret, channel, ts)
	return map[string]any{
		"time":    ts,
		"channel": channel,
		"event":   "subscribe",
		"payload": payload,
		"auth": map[string]string{
			"method": "api_key",
			"KEY":    g.apiKey,
			"SIGN":   sign,
		},
	}
}

// gateWSSign 订阅签名：hex(HMAC-SHA512(secret,"channel=<ch>&event=subscribe&time=<unix秒>"))。
func gateWSSign(secret, channel string, ts int64) string {
	msg := fmt.Sprintf("channel=%s&event=subscribe&time=%d", channel, ts)
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

// fetchUID 取 futures 账户 user id（签名 GET /futures/usdt/accounts，exec.GateSign 共享）。
func (g *GatePrivateWS) fetchUID(ctx context.Context) (string, error) {
	const endpoint = "/futures/usdt/accounts"
	ts := g.now().Unix()
	body, err := g.get(ctx, endpoint, ts)
	if err != nil {
		return "", err
	}
	var acc struct {
		User json.Number `json:"user"`
	}
	if err := json.Unmarshal(body, &acc); err != nil {
		return "", fmt.Errorf("accounts json: %w", err)
	}
	if acc.User.String() == "" {
		return "", fmt.Errorf("accounts response missing user field")
	}
	return acc.User.String(), nil
}

// gateQuanto contract→quanto_multiplier lazy 缓存（executor 同款；公开端点签名无害）。
func (g *GatePrivateWS) gateQuanto(ctx context.Context, contract string) (float64, error) {
	if q, ok := g.quanto[contract]; ok {
		return q, nil
	}
	body, err := g.get(ctx, "/futures/usdt/contracts/"+contract, g.now().Unix())
	if err != nil {
		return 0, err
	}
	var spec struct {
		QuantoMultiplier string `json:"quanto_multiplier"`
	}
	if err := json.Unmarshal(body, &spec); err != nil {
		return 0, fmt.Errorf("contract spec json: %w", err)
	}
	q, err := strconv.ParseFloat(spec.QuantoMultiplier, 64)
	if err != nil || q <= 0 {
		return 0, fmt.Errorf("contract %s quanto_multiplier=%q invalid", contract, spec.QuantoMultiplier)
	}
	g.quanto[contract] = q
	return q, nil
}

// get 签名 GET（exec.GateSign 与 REST 路径同口径：urlPath=/api/v4+endpoint）。
func (g *GatePrivateWS) get(ctx context.Context, endpoint string, ts int64) ([]byte, error) {
	urlPath := "/api/v4" + endpoint
	sign := exec.GateSign(g.apiSecret, "GET", urlPath, "", "", ts)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("KEY", g.apiKey)
	req.Header.Set("Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("SIGN", sign)
	resp, err := g.rest.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, truncateStr(string(data), 200))
	}
	return data, nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
