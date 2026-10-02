// binance_user_ws.go —— D-450 S5：Binance 用户数据流经 cex-proxy。
// listenKey 生命周期：POST {base}/v1/listenKey 取 key（base 已含 /fapi 段——
// DB 实拍 real=proxy/fapi（demofapi 支路 D-516 收编删除）；审计 R1 修订，勿再拼 /fapi）
// → wss 连接 → PUT 每 30min 续租（官方 60min 失效）→ 续租失败/读断 → 重新取 key
// 重连（key 不可复用跨重连）。事件 ORDER_TRADE_UPDATE / ACCOUNT_UPDATE。
// 订单/持仓频道无心跳——静默属正常态，存活判定=ping 20s+pong 顺延读超时 60s。
// spot 走 ws-api 支路（binance_user_ws_spot.go，D-459：REST userDataStream 410 下线，
// session.logon 拒 HMAC key——实证改走 userDataStream.start/ping 免签名管理面）。
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

	"arb/internal/bus"
	"arb/internal/dephealth"
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
	hooks      BinanceUserHooks // D-469a S3：a.m 事件原因 kick 回调（nil 安全）

	// balLedger D-542：可用资金活账本（ACCOUNT_UPDATE/outboundAccountPosition 喂入；
	// 每轮重连标脏，dashboard 弹窗脏/空走 REST 校准）。nil 安全。
	balLedger  *bus.BalanceLedger
	rest       *http.Client
	dial       func(ctx context.Context, wsURL string) (*websocket.Conn, error)
	renewEvery time.Duration // 续租周期（默认 30min；测试注入缩短）
	now        func() time.Time
	// onReconnect D-004 P1#6：连接就绪回调（断流后事件漏发补救→对账 kick；首连也触发）。nil 安全。
	onReconnect func()
}

// SetBalanceLedger D-542：注入活账本（装配一次；nil 安全）。
func (b *BinanceUserWS) SetBalanceLedger(l *bus.BalanceLedger) { b.balLedger = l }

// SetOnReconnect D-004 P1#6：注入就绪回调（每次连接就绪调一次，含首连）。nil 安全。
func (b *BinanceUserWS) SetOnReconnect(f func()) { b.onReconnect = f }

// depKey D-579 R2 健康面键（用户流无订阅帧，就绪=listenKey+流连接建立）。
func (b *BinanceUserWS) depKey() string { return "binance:ws-" + b.venueType + "-user" }

// balPoolKey 活账本键=裸 canonical 池键（venueKey 撞键 #id 不进账本——账户级口径）。
func (b *BinanceUserWS) balPoolKey() string {
	return "binance:" + b.venueType
}

// NewBinanceUserWS 创建采集器。
// BinanceUserHooks D-469a S3：ACCOUNT_UPDATE a.m 事件原因回调（nil 安全）——
// OnFundingFee=FUNDING_FEE（kick binance income）；OnIO=DEPOSIT/WITHDRAW（kick cex-io）。
type BinanceUserHooks struct {
	OnFundingFee func()
	OnIO         func()
	// D-569 包B S7：MARGIN_CALL 整帧转发（payload=全帧 data；schema 官方不可得故不解析）。
	OnMarginCall func(payload []byte)
	// OnPositionClosed D-015 ②（R1）：ACCOUNT_UPDATE a.P pa=0 行=该 symbol+posSide 已平
	// （手动/强平/ADL 同形态，a.m 记 ORDER/AUTODELEVERAGE 等原因）→ 落 CLOSE EXTERNAL + recon.Kick。
	OnPositionClosed func(venueKey, symbol, posSide string)
}

func NewBinanceUserWS(apiKey, apiSecret, baseURL, venueType, venueKey string, simulated bool, sink VenueSnapshotSink, events PrivateEventSink, oc *OrderEventCache, pc *PositionEventCache, hooks BinanceUserHooks) *BinanceUserWS {
	b := &BinanceUserWS{
		apiKey: apiKey, baseURL: baseURL, venueType: venueType, venueKey: venueKey,
		simulated: simulated, sink: sink, events: events, hooks: hooks,
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

// listenKeyEndpoint REST 端点（perp=base+/v1/listenKey；spot 恒走 runOnceSpot ws-api
// 控制连接不达此——D-569 包B S7 实证清理，spot REST userDataStream 已 410）。
func (b *BinanceUserWS) listenKeyEndpoint() string {
	return b.baseURL + "/v1/listenKey"
}

// WSURL listenKey → WS 地址（D-517 S3 fstream 迁移映射：perp→/private/ws、spot→/spotws；
// baseURL 只取 scheme+host——fapi REST 段不进 WS URL。simulated=t fail-closed，D-516 S2）。
func (b *BinanceUserWS) WSURL(listenKey string) (string, error) {
	if b.simulated {
		return "", fmt.Errorf("binance user ws: simulated config refused (demo 域已收编 D-516)")
	}
	u, err := url.Parse(b.baseURL)
	if err != nil {
		return "", fmt.Errorf("parse baseURL: %w", err)
	}
	scheme := "wss"
	if u.Scheme == "http" {
		scheme = "ws"
	}
	var path string
	switch b.venueType {
	case "perp":
		path = "/private/ws/" + listenKey // D-517 S3：旧 /ws → /private（fstream 端点拆分迁移）
	case "spot":
		path = "/spotws/ws/" + listenKey // spot 独立 host，不在本次 fstream 迁移面
	default:
		return "", fmt.Errorf("binance user ws: unsupported venueType=%q", b.venueType)
	}
	return fmt.Sprintf("%s://%s%s", scheme, u.Host, path), nil
}

// dialFailover 双端点回退：主 dial 败→BINANCE_PROXY_URL_BACKUP 换 host 重试；
// 每连接自主端起，主端恢复自动回主（无粘滞）。
func (b *BinanceUserWS) dialFailover(ctx context.Context, wsURL string) (*websocket.Conn, error) {
	conn, err := b.dial(ctx, wsURL)
	if err == nil || ctx.Err() != nil {
		return conn, err
	}
	alt := b.backupWSURL(wsURL)
	if alt == "" {
		return conn, err
	}
	log.Printf("[binance-user-ws] dial %s failed: %v（试备用端点 %s）", wsURL, err, alt)
	return b.dial(ctx, alt)
}

// backupWSURL wsURL host → BINANCE_PROXY_URL_BACKUP host（空/非法 → ""=无备用候选）。
// D-519：host 换段收敛共享件 swapURLHost（gate 私有同法）。
func (b *BinanceUserWS) backupWSURL(wsURL string) string {
	return swapURLHost(wsURL, "BINANCE_PROXY_URL_BACKUP")
}

func (b *BinanceUserWS) runOnce(ctx context.Context) error {
	b.balLedger.MarkDirty(b.balPoolKey()) // D-542：重连即标脏——死链期不读尸值
	if b.simulated {
		return fmt.Errorf("binance user ws: simulated config refused (demo 域已收编 D-516)")
	}
	if b.venueType == "spot" {
		return b.runOnceSpot(ctx)
	}
	key, err := b.obtainListenKey(ctx)
	if err != nil {
		return fmt.Errorf("listenKey: %w", err)
	}
	wsURL, err := b.WSURL(key)
	if err != nil {
		return err
	}
	conn, err := b.dialFailover(ctx, wsURL)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	log.Printf("[binance-user-ws] connected (venue=%s:%s simulated=%v)", "binance", b.venueType, b.simulated)
	dephealth.Mark(b.depKey(), true, nil) // D-579 R2：listenKey+流连接就绪（非裸 TCP）
	if b.onReconnect != nil {
		b.onReconnect() // D-004 P1#6：重连成功→对账补漏 kick
	}

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
				_ = conn.SetWriteDeadline(b.now().Add(wsWriteDeadline)) // D-587
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
