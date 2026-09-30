// gate_private_ws.go —— D-450 S2：Gate futures 私有 WS 经 cex-proxy。
// 订阅 futures.balances/positions/orders/usertrades+风险频道族（payload 按频道分派：
// balances=[uid]、position_adl_rank=["!all"] 无 uid、其余=[uid,"!all"]，
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
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/dephealth"
	"arb/internal/httpx"
	"arb/internal/safego"

	"github.com/gorilla/websocket"
)

const gatePrivateReadIdle = 60 * time.Second

// gatePrivateChannels D-450 订阅面（payload 形制见 subscribeFrame 按频道分派）。
// D-570 包C S7：扩 5 风险频道（强平/平仓/ADL 双频道/风控限额降档——全整帧转发 hooks）。
var gatePrivateChannels = []string{
	"futures.balances",
	"futures.positions",
	"futures.orders",
	"futures.usertrades",
	"futures.liquidates",
	"futures.position_closes",
	"futures.auto_deleverages",
	"futures.position_adl_rank",
	"futures.reduce_risk_limits",
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
	// balLedger D-542：可用资金活账本（futures.balances 喂入；每轮重连标脏）。nil 安全。
	balLedger *bus.BalanceLedger

	dial func(ctx context.Context, wsURL string) (*websocket.Conn, error) // 测试注入
	rest *http.Client                                                     // uid/quanto 查询（测试注入）
	now  func() time.Time
	// D-570 包C S7：风险事件 hooks（liquidates/position_closes/ADL/reduce_risk_limits 整帧转发；nil 安全）。
	hooks GatePrivateHooks
	// D-570 包C S6③：quanto 加锁——contract_info 失效回调跨 goroutine（公共 WS loop）
	// 与读循环 gateQuanto lazy 写并发。
	quantoMu sync.Mutex
	quanto   map[string]float64 // contract→quanto_multiplier（executor 同款 lazy 缓存）

	// writeMu D-477：gorilla conn 单写者契约——ping goroutine（20s WriteMessage）与订阅
	// 写（runOnce WriteJSON）共用 conn，无互斥=数据竞争（race×3 实证）；conn 写统一走
	// g.writeConn 串行化。
	writeMu sync.Mutex
	// onReconnect D-004 P1#6：订阅就绪回调（断流后事件漏发补救→对账 kick）。nil 安全。
	onReconnect func()
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

// SetBalanceLedger D-542：注入活账本（装配一次；nil 安全）。
func (g *GatePrivateWS) SetBalanceLedger(l *bus.BalanceLedger) { g.balLedger = l }

// SetGateHooks D-570 包C S7：注入风险事件 hooks（镜像 SetBalanceLedger 先例；nil 安全）。
func (g *GatePrivateWS) SetGateHooks(h GatePrivateHooks) { g.hooks = h }

// SetOnReconnect D-004 P1#6：注入就绪回调（每次连接+订阅成功调一次，含首连）。nil 安全。
func (g *GatePrivateWS) SetOnReconnect(f func()) { g.onReconnect = f }

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
			dephealth.Mark("gate:ws-private", false, err) // D-579 R2：私有流经 runOnce 不经 RunWS 骨架，自挂点
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
	g.balLedger.MarkDirty("gate:perp") // D-542：重连即标脏——死链期不读尸值
	uid, err := g.fetchUID(ctx)
	if err != nil {
		return fmt.Errorf("fetch uid: %w", err)
	}
	conn, err := g.dial(ctx, g.WSURL())
	if err != nil && ctx.Err() == nil {
		// D-519 D4：cex-proxy 双端点回退——主 URL 拨失败换 ARB_CEX_WS_BASE_BACKUP host
		// 重试一次（path 不变，同 binance backupWSURL 模式；env 空=零行为变化）。
		if alt := swapURLHost(g.WSURL(), "ARB_CEX_WS_BASE_BACKUP"); alt != "" {
			log.Printf("[gate-ws] dial %s failed: %v（试备用端点 %s）", g.WSURL(), err, alt)
			conn, err = g.dial(ctx, alt)
		}
	}
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
				g.writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteDeadline)) // D-587
				err := conn.WriteMessage(websocket.PingMessage, nil)
				g.writeMu.Unlock()
				if err != nil {
					return
				}
			case <-done:
				return
			}
		}
	})

	// 逐频道签名订阅（每频道独立 auth 帧——签名含 channel 名）。
	for _, ch := range gatePrivateChannels {
		g.writeMu.Lock()
		_ = conn.SetWriteDeadline(time.Now().Add(wsWriteDeadline)) // D-587
		err := conn.WriteJSON(g.subscribeFrame(ch, uid))
		g.writeMu.Unlock()
		if err != nil {
			return fmt.Errorf("subscribe %s: %w", ch, err)
		}
	}
	dephealth.Mark("gate:ws-private", true, nil) // D-579 R2：逐频道签名订阅帧全部发出=就绪点
	if g.onReconnect != nil {
		g.onReconnect() // D-004 P1#6：重连成功→对账补漏 kick
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
// balances→[uid]；position_adl_rank→["!all"] 无 uid 前缀（快照 :7007 真形，D-570C-R2）；
// 其余→[uid,"!all"]。
func (g *GatePrivateWS) subscribeFrame(channel, uid string) map[string]any {
	payload := []string{uid, "!all"}
	switch channel {
	case "futures.balances":
		payload = []string{uid}
	case "futures.position_adl_rank":
		payload = []string{"!all"}
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

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
