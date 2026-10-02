// okx_private_ws.go D-383 B2：OKX private account channel 订阅（余额事件驱动持久层）。
// login（HMAC-SHA256 签名）→ 订阅 {channel:"account", ccy:"USDT"} → 推送落
// fin_venue_account_snapshot（venue=okx, venue_type=unified, src=okx_event）。
// 字段映射与 REST accountOKX 同口径（D-364 USDT-only：equity=Σ details[USDT].eqUsd，
// balance=equity，currency=USDT）——SSOT B.3 锚「对拍两行差异<1%」以此为准；REST 轮询
// 节拍保留双轨兜底不拆旧链路。login/权限错误 fail-loud 重连（不静默退化成无事件）；
// 凭据不进日志。断流守卫：订阅成功即计基准，>okxPrivateStaleAfter 无 push → warn
// （okxPrivateWarnCooldown 冷却，D-370 断连恢复模式；OKX account channel 订阅即推首帧
// 快照，静默超阈值=订阅死而非静默账户）。
package exchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"arb/internal/bus"
	"arb/internal/dephealth"
	"arb/internal/safego"
	"arb/internal/store/pgstore"

	"github.com/gorilla/websocket"
)

const (
	// okxPrivateWSURL SSOT §5 B.2 钉死端点（v5 private）。
	okxPrivateWSURL = "wss://ws.okx.com:8443/ws/v5/private"
	// okxPrivateStaleAfter 断流判定：无 push 超此时长 warn（SSOT「>N min」钉值）。
	okxPrivateStaleAfter = 30 * time.Minute
	// okxPrivateWarnCooldown 断流 warn 单次冷却（D-370 模式）。
	okxPrivateWarnCooldown = 30 * time.Minute
	// okxPrivateReadIdle 读空闲上限（R2 返修）：pong 到即顺延——静默账户靠 pong 保活
	// 不再被固定读超时空转重连；真死链（无 pong）超时判死重连。
	okxPrivateReadIdle = 60 * time.Second
)

// VenueSnapshotSink D-383 B2：WS 推送落库面（*pgstore.Store 原生实现）。
type VenueSnapshotSink interface {
	InsertVenueAccountSnapshots(ctx context.Context, snaps []pgstore.VenueAccountSnapshot) error
}

// OKXPrivateHooks D-469a S1：私有事件回调（nil 安全跳过）——
// OnFundingFee=balance_and_position eventType=funding_fee（结算费到账 kick income）；
// OnAccountPush=account 帧（余额变动 kick acctMon）；OnOrderPush=orders 帧。
type OKXPrivateHooks struct {
	OnFundingFee  func()
	OnAccountPush func()
	OnOrderPush   func()
	// OnLiqWarning D-566 包B S7：liquidation-warning 帧 → 三键透传（缺席空串）。
	OnLiqWarning func(instID, posSide, mgnRatio string)
	// OnPositionClosed D-015 ②：positions 帧 pos=0 行=该 instId+posSide 已平
	// （手动/强平/ADL 同形态）→ 装配层落 CLOSE EXTERNAL 标记 + recon.Kick。
	OnPositionClosed func(venueKey, instID, posSide string)
}

// OKXPrivateWS OKX 私有 channel 采集器（D-469a 扩四频道：account/balance_and_position/
// positions/orders，args 形制 S0 真户实证）。
type OKXPrivateWS struct {
	apiKey     string
	apiSecret  string
	passphrase string
	simulated  bool
	sink       VenueSnapshotSink
	venueKey   string
	events     PrivateEventSink
	orderCache *OrderEventCache
	posCache   *PositionEventCache
	// balLedger D-542：可用资金活账本（account 频道 availBal 喂入；每轮重连标脏）。nil 安全。
	balLedger   *bus.BalanceLedger
	hooks       OKXPrivateHooks
	dialer      *websocket.Dialer
	proxyHeader http.Header // 代理模式=X-Proxy-Token 握口头；直连=nil
	wsURLs      []string    // 有序候选（cex-proxy 双端点 failover；[0]=主端，ARB_CEX_WS_BASE 置位→经 /okxws）
	now         func() time.Time
	// ctValFor D-477 S3（GAP-B4）：okx 合约面值解析器注入——构造点经 resolveExchangeBaseURL
	// 取与 executor 同一 baseURL 源（离线/代理拓扑下不再双源分叉）。nil=跳过换算保持张数+warn。
	ctValFor func(ctx context.Context, instID string) (float64, error)
	// onReconnect D-004 P1#6：订阅就绪（断流后事件漏发补救→对账 kick）。nil 安全。
	onReconnect func()
}

// NewOKXPrivateWS 创建采集器（凭据=REST querier 同源 exchange_configs，不新增 secret 配置项）。
// venueKey=撞键归一键（reconcile.CexVenueKey 同源，posCache/orderCache 键）；
// events=审计表写口（nil 安全）；hooks nil 安全跳过。
func NewOKXPrivateWS(apiKey, apiSecret, passphrase string, simulated bool, sink VenueSnapshotSink, venueKey string, events PrivateEventSink, oc *OrderEventCache, pc *PositionEventCache, hooks OKXPrivateHooks) *OKXPrivateWS {
	proxied := cexWSBase() != ""
	d, h := wsDialer(proxied)
	return &OKXPrivateWS{
		apiKey: apiKey, apiSecret: apiSecret, passphrase: passphrase, simulated: simulated,
		sink: sink, venueKey: venueKey, events: events, orderCache: oc, posCache: pc, hooks: hooks,
		dialer: d, proxyHeader: h,
		wsURLs: wsViaProxyURLs("/okxws/ws/v5/private", okxPrivateWSURL),
		now:    time.Now,
	}
}

// SetBalanceLedger D-542：注入活账本（装配一次；nil 安全）。
func (o *OKXPrivateWS) SetBalanceLedger(l *bus.BalanceLedger) { o.balLedger = l }

// SetOnReconnect D-004 P1#6：注入就绪回调（每次连接+订阅成功调一次，含首连——
// 新实例首连同样刚历过断流窗口；kick 合流无成本）。nil 安全。
func (o *OKXPrivateWS) SetOnReconnect(f func()) { o.onReconnect = f }

// SetCtValFor D-477 S3：注入 ctVal 解析器（GAP-B4 单源化；nil=保持张数+warn 降级）。
func (o *OKXPrivateWS) SetCtValFor(f func(ctx context.Context, instID string) (float64, error)) {
	o.ctValFor = f
}

// Run 重连循环（5s 退避，仿 okx_ws 骨架）。阻塞至 ctx 取消。
func (o *OKXPrivateWS) Run(ctx context.Context) {
	for {
		if err := o.runOnce(ctx); err != nil {
			dephealth.Mark("okx:ws-private", false, err) // D-579 R2：私有流自挂点（不经 RunWS）
			log.Printf("[okx-private-ws] reconnect after error: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (o *OKXPrivateWS) runOnce(ctx context.Context) error {
	o.balLedger.MarkDirty("okx:unified") // D-542：重连即标脏——死链期不读尸值（统一账户口径）
	if o.simulated {
		// D-516 S3：demo 支路删除——simulated=t 一律 fail-closed，绝不拨 demo 端点。
		return fmt.Errorf("okx private ws: simulated config refused (demo 域已收编 D-516)")
	}
	header := o.proxyHeader.Clone()
	var conn *websocket.Conn
	var err error
	for _, u := range o.wsURLs {
		conn, _, err = o.dialer.DialContext(ctx, u, header)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			break // D-519 M1/R2：守卫须在拨号后——循环顶查会让预取消 ctx 零拨号带 nil conn 落穿（dial 错误检查放行→WriteJSON nil panic，复审探针实证）
		}
		log.Printf("[okx-private-ws] dial %s failed: %v（尝试下一候选）", u, err)
	}
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// login（SSOT §5 B.2：sign=base64(HMAC-SHA256(secret, ts+"GET"+"/users/self/verify"))）。
	ts := okxPrivateLoginTs(time.Now())
	_ = conn.SetWriteDeadline(time.Now().Add(wsWriteDeadline)) // D-587
	if err := conn.WriteJSON(o.loginFrame(ts)); err != nil {
		return fmt.Errorf("login write: %w", err)
	}

	// R2 返修：读超时由 pong 顺延（pong 到→now+60s），ping 20s 保活不变；连接期只设一次。
	_ = conn.SetReadDeadline(time.Now().Add(okxPrivateReadIdle))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(okxPrivateReadIdle))
	})

	stale := newStaleGuard()
	done := make(chan struct{})
	defer close(done)
	safego.Go("okx_private_ws:ping", func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteDeadline)) // D-587
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
				// 断流守卫：连接活（ping 通）但订阅面静默 → warn 单次冷却（REST 兜底仍在）。
				if stale.warnDue(time.Now(), okxPrivateStaleAfter, okxPrivateWarnCooldown) {
					log.Printf("[okx-private-ws] no account push >%s（订阅死嫌疑；REST 兜底双轨仍在）", okxPrivateStaleAfter)
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
		var msg wsMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Event {
		case "error":
			// fail-loud：login/订阅错误（权限不足类）带码报出重连，不静默退化成无事件（SSOT B.4）；
			// 仅码与文案进日志，凭据零暴露（B4 锚）。
			var e struct {
				Code string `json:"code"`
				Msg  string `json:"msg"`
			}
			_ = json.Unmarshal(data, &e)
			return fmt.Errorf("okx private event error: code=%s msg=%s", e.Code, e.Msg)
		case "login":
			log.Printf("[okx-private-ws] login ok (simulated=%v)", o.simulated)
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteDeadline)) // D-587
			if err := conn.WriteJSON(map[string]any{
				"op":   "subscribe",
				"args": okxPrivateSubscribeArgs(),
			}); err != nil {
				return fmt.Errorf("subscribe: %w", err)
			}
		case "subscribe":
			stale.touch(time.Now())                     // 订阅成功=断流基准点（OKX account channel 订阅即推首帧）
			dephealth.Mark("okx:ws-private", true, nil) // D-579 R2：就绪点=订阅生效非裸 TCP
			log.Printf("[okx-private-ws] subscribed %s", msg.Arg.Channel)
			if o.onReconnect != nil {
				o.onReconnect() // D-004 P1#6：重连成功→对账补漏 kick
			}
		case "":
			// 数据推送——D-469a S1 按 arg.channel 分派（S0：positions/account eventType
			// 在帧顶层；balance_and_position eventType 在 data[i] 内）。
			stale.touch(time.Now())
			o.handleData(ctx, msg.Arg.Channel, data, msg.Data)
		default:
			continue
		}
	}
}

// staleGuard D-383 B2 断流守卫（D-370 warn 单次冷却模式，纯状态可测）。
type staleGuard struct {
	lastPush time.Time
	lastWarn time.Time
	seen     bool
}

func newStaleGuard() staleGuard { return staleGuard{} }

func (g *staleGuard) touch(now time.Time) {
	g.lastPush, g.seen = now, true
}

// idle 无 push 时长（订阅前未见过 push → 0，不告警）。
func (g *staleGuard) idle(now time.Time) time.Duration {
	if !g.seen {
		return 0
	}
	return now.Sub(g.lastPush)
}

// warnDue 静默超 idle 阈值且距上次 warn≥cooldown → 报一次并记冷却位。
func (g *staleGuard) warnDue(now time.Time, idle, cooldown time.Duration) bool {
	if g.idle(now) < idle || now.Sub(g.lastWarn) < cooldown {
		return false
	}
	g.lastWarn = now
	return true
}

// okxPrivateLoginTs login timestamp 串（R3 返修：SSOT §5 B.2 订正=Unix epoch 秒串；
// 原 ISO8601 系 REST 头格式误植——OKX WS login 60004 Invalid timestamp 在产实证）。
func okxPrivateLoginTs(now time.Time) string {
	return strconv.FormatInt(now.Unix(), 10)
}

// okxPrivateLoginPrehash OKX login 签名原串（SSOT §5 B.2：ts+"GET"+"/users/self/verify"）。
// R1 返修抽取：调用点经此构造 prehash，锚钉组合而非只测纯函数。
func okxPrivateLoginPrehash(ts string) string {
	return ts + "GET" + "/users/self/verify"
}

// loginFrame 组装完整 login 帧（含签名）。
func (o *OKXPrivateWS) loginFrame(ts string) map[string]any {
	return okxPrivateLoginFrame(o.apiKey, o.passphrase, ts, okxPrivateSign(o.apiSecret, okxPrivateLoginPrehash(ts)))
}

// okxPrivateSign OKX 签名（与 reconcile.okxSign 同算法：base64(HMAC-SHA256(secret, prehash))）。
func okxPrivateSign(secret, prehash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(prehash))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// okxPrivateLoginFrame login 帧（SSOT §5 B.2 逐字；凭据只进帧不进日志，B4 锚）。
func okxPrivateLoginFrame(apiKey, passphrase, ts, sign string) map[string]any {
	return map[string]any{
		"op": "login",
		"args": []map[string]string{{
			"apiKey":     apiKey,
			"passphrase": passphrase,
			"timestamp":  ts,
			"sign":       sign,
		}},
	}
}
