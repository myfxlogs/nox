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

// OKXPrivateWS OKX 私有账户 channel 采集器。
type OKXPrivateWS struct {
	apiKey     string
	apiSecret  string
	passphrase string
	simulated  bool
	sink       VenueSnapshotSink
	dialer     websocket.Dialer
}

// NewOKXPrivateWS 创建采集器（凭据=REST querier 同源 exchange_configs，不新增 secret 配置项）。
func NewOKXPrivateWS(apiKey, apiSecret, passphrase string, simulated bool, sink VenueSnapshotSink) *OKXPrivateWS {
	return &OKXPrivateWS{
		apiKey: apiKey, apiSecret: apiSecret, passphrase: passphrase, simulated: simulated,
		sink: sink,
		dialer: websocket.Dialer{
			Proxy:            http.ProxyFromEnvironment,
			HandshakeTimeout: 10 * time.Second,
		},
	}
}

// Run 重连循环（5s 退避，仿 okx_ws 骨架）。阻塞至 ctx 取消。
func (o *OKXPrivateWS) Run(ctx context.Context) {
	for {
		if err := o.runOnce(ctx); err != nil {
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
	header := http.Header{}
	if o.simulated {
		// 模拟盘：与 REST 同语义（demo key 对实盘域名带 x-simulated-trading:1，D-054）。
		header.Set("x-simulated-trading", "1")
	}
	conn, _, err := o.dialer.DialContext(ctx, okxPrivateWSURL, header)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// login（SSOT §5 B.2：sign=base64(HMAC-SHA256(secret, ts+"GET"+"/users/self/verify"))）。
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
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
			if err := conn.WriteJSON(map[string]any{
				"op":   "subscribe",
				"args": []map[string]string{{"channel": "account", "ccy": "USDT"}},
			}); err != nil {
				return fmt.Errorf("subscribe: %w", err)
			}
		case "subscribe":
			stale.touch(time.Now()) // 订阅成功=断流基准点（OKX account channel 订阅即推首帧）
			log.Printf("[okx-private-ws] subscribed account channel")
		case "":
			// 数据推送。
			stale.touch(time.Now())
			if msg.Arg.Channel != "account" || len(msg.Data) == 0 {
				continue
			}
			row, ok := okxAccountPushRow(msg.Data[0], time.Now())
			if !ok {
				continue
			}
			if err := o.sink.InsertVenueAccountSnapshots(ctx, []pgstore.VenueAccountSnapshot{row}); err != nil {
				log.Printf("[okx-private-ws] insert snapshot: %v", err) // 写失败不杀读循环，下条推送重试
			}
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

// okxAccountPushRow account channel 推送 → 快照行（纯函数）。
// 口径=REST accountOKX 同源（D-364 S2）：equity=Σ details[USDT].eqUsd（解析失败跳该币），
// balance=equity，currency=USDT，free_margin 不采（REST 同缺省）；details 无 USDT 行 →
// eq=0 行照落（同 REST 不 panic 语义）。
func okxAccountPushRow(data map[string]any, now time.Time) (pgstore.VenueAccountSnapshot, bool) {
	raw, err := json.Marshal(data)
	if err != nil {
		return pgstore.VenueAccountSnapshot{}, false
	}
	var d struct {
		Details []struct {
			Ccy   string `json:"ccy"`
			EqUsd string `json:"eqUsd"`
		} `json:"details"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return pgstore.VenueAccountSnapshot{}, false
	}
	eq := 0.0
	for _, det := range d.Details {
		if det.Ccy != "USDT" {
			continue
		}
		if usd, perr := strconv.ParseFloat(det.EqUsd, 64); perr == nil {
			eq += usd
		}
	}
	return pgstore.VenueAccountSnapshot{
		Venue: "okx", VenueType: "unified",
		BalanceRaw: eq, EquityRaw: eq,
		Currency: "USDT", FetchedAt: now, Src: "okx_event",
	}, true
}
