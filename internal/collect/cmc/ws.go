// CMC WebSocket 事件驱动采集（D-225 v2.1 事实重设计）。
// WsClient 维持 wss://connectapi.cmcmarkets.com/ws 长连接，订阅 type 5/6/7，
// 事件落库 + fact 发射（Src="cmc_ws"）；断连自动重连+重订阅。
// AccountCollector.Poll 在 ws 健康时短路（REST 兜底降级）。
// v2.1 变更：type6 全量自持（落库行从 type6 取）、type5 降为交叉校验、
// type7 首帧种子（取消 REST 播种）、patch 三深度分发、价格事件化（evaluationAskPrice）、
// alerts 采集、守卫只看 type6、closeout 内存重算与落库解耦。
package cmc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"arb/internal/fact"
	"arb/internal/safego"
	"arb/internal/store/pgstore"

	"github.com/gorilla/websocket"
)

// DefaultWsBase CMC WebSocket 端点（D-223 设计 C-Final + R6 返修：URL 必须带 ?partialUpdateEventTypes=ALL，
// 否则连接成功但事件全静默丢弃——实证 docs/cmc/证据/connectapi-ws-帧实证-2026-09-04.log）。
const DefaultWsBase = "wss://connectapi.cmcmarkets.com/ws?partialUpdateEventTypes=ALL"

const (
	wsHeartbeatInterval = 5 * time.Second
	wsReadTimeout       = 12 * time.Second
	// D-225 v2.1 节流参数
	wsSummaryThrottle   = 60 * time.Second // type6 落库节流
	wsSummaryMinDelta   = 0.01             // totalAccountValue 变化阈值
	wsSummaryForceFlush = 10 * time.Minute // 强制一行（时序锚）
	wsPriceMinDelta     = 0.001            // 价格变化 >0.1%
	wsPriceThrottle     = 2 * time.Second  // 价格落库最小间隔
	wsPriceHeartbeat    = 4 * time.Minute  // 心跳行（保时序连续，<watchdog 5min 余量）
	wsPosFlushInterval  = 15 * time.Second // 持仓落库节流（替换旧 2s burst）
	wsSeedFallbackDelay = 5 * time.Second  // 订阅后无首帧 → REST 播种兜底
	wsKickDebounce      = 30 * time.Second
	wsCrossCheckDrift   = 0.005 // type5/6 漂移 >0.5%
	wsCrossCheckStrikes = 3     // 连续 3 窗触发告警
)

var ( // 新鲜度守卫参数（C2 路径②）+ 退避：var 便于测试覆写
	wsGuardInterval = 60 * time.Second
	wsGuardStale    = 10 * time.Minute
	wsBackoff       = func(attempt int) time.Duration {
		d := time.Duration(1<<uint(attempt)) * time.Second
		if d > 60*time.Second {
			return 60 * time.Second
		}
		return d
	}
)

// Snapshot D-225 v2.1 S5：实时扇出只读 accessor（R10 seam 复用）。
type Snapshot struct {
	Summary   accountSummaryJSON
	Positions []positionJSON
	EvalAsk   float64
	Ts        time.Time
}

type WsClient struct {
	auth         jwtProvider
	accountID    int64
	store        *pgstore.Store
	wsBase       string
	restBase     string
	healthy      atomic.Bool
	kickHistory  func()
	sink         func(ctx context.Context, facts []fact.Fact) error
	marketOpen   func() bool
	marketStatus func() string  // D-225 v2.1 S5：WS 价格行 market_status 注入
	onEvent      func(Snapshot) // D-225 v2.1 S5：事件即触发回调（R10 接线）

	positionsMu sync.Mutex
	positions   []positionJSON

	// type6 账户主数据源（全量自持）
	summaryMu          sync.Mutex
	lastSummary        accountSummaryJSON // 从 type6 解析的最新全量
	lastSummaryPersist time.Time
	lastType6Val       float64
	lastAlerts         json.RawMessage // 内存最新 alerts 原文
	alertsChanged      bool            // 进入/退出预警事件待记
	lastSummaryTs      time.Time       // type6 payload timestamp

	// 价格事件
	priceMu        sync.Mutex
	lastPriceWrite time.Time
	lastPriceVal   float64
	priceHeartbeat *time.Timer
	evalAsk        float64
	evalAskTs      time.Time

	// 持仓落库节流
	flushMu      sync.Mutex
	flushTimer   *time.Timer
	lastPosFlush time.Time

	// 新鲜度守卫（只看 type6）
	lastEvent6Mu sync.Mutex
	lastEvent6   time.Time

	// type5/6 交叉校验
	crossCheckMu      sync.Mutex
	crossCheckStrikes int
	lastType5Val      float64

	// patch 异常计数
	patchWarnCount atomic.Int64

	// 首帧种子
	seedDone atomic.Bool

	kickMu       sync.Mutex
	lastKickTime time.Time
	connMu       sync.Mutex
}

func NewWsClient(auth jwtProvider, accountID int64, store *pgstore.Store, wsBase, restBase string) *WsClient {
	return &WsClient{
		auth: auth, accountID: accountID, store: store, wsBase: wsBase, restBase: restBase,
		marketOpen:   func() bool { return true }, // 保守默认：无缓存时视为市场时段
		marketStatus: func() string { return "" }, // 默认无注入
		lastAlerts:   json.RawMessage("[]"),
	}
}

func (w *WsClient) SetKickHistory(fn func())                                      { w.kickHistory = fn }
func (w *WsClient) SetSink(fn func(ctx context.Context, facts []fact.Fact) error) { w.sink = fn }
func (w *WsClient) SetMarketOpen(fn func() bool) {
	if fn != nil {
		w.marketOpen = fn
	}
}
func (w *WsClient) SetMarketStatus(fn func() string) {
	if fn != nil {
		w.marketStatus = fn
	}
}
func (w *WsClient) SetOnEvent(fn func(Snapshot)) { w.onEvent = fn }
func (w *WsClient) IsHealthy() bool              { return w.healthy.Load() }

// LatestSnapshot D-225 v2.1 S5：只读 accessor（R10 seam 复用）。
func (w *WsClient) LatestSnapshot() Snapshot {
	w.summaryMu.Lock()
	summary := w.lastSummary
	alerts := w.lastAlerts
	ts := w.lastSummaryTs
	w.summaryMu.Unlock()
	w.positionsMu.Lock()
	posCopy := make([]positionJSON, len(w.positions))
	copy(posCopy, w.positions)
	w.positionsMu.Unlock()
	w.priceMu.Lock()
	evalAsk := w.evalAsk
	evalTs := w.evalAskTs
	w.priceMu.Unlock()
	snap := Snapshot{Summary: summary, Positions: posCopy, EvalAsk: evalAsk}
	if !evalTs.IsZero() {
		snap.Ts = evalTs
	} else if !ts.IsZero() {
		snap.Ts = ts
	} else {
		snap.Ts = time.Now()
	}
	_ = alerts // alerts 在 summary 内（lastSummary 不含 alerts，alerts 独立存）
	return snap
}

func (w *WsClient) Run(ctx context.Context) {
	safego.Go("cmc-ws:guard", func() { w.startGuard(ctx) })
	for attempt := 0; ; attempt++ {
		if err := w.runOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			wait := wsBackoff(attempt)
			log.Printf("[cmc-ws] runOnce failed (attempt %d): %v, retry in %s", attempt+1, err, wait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}
		return
	}
}

func (w *WsClient) startGuard(ctx context.Context) {
	// D-225 v2.1：守卫只看 type6 事件（F1/F2 周期推，沉默=真异常）；type7 沉默≠异常（F3 变更驱动）。
	ticker := time.NewTicker(wsGuardInterval)
	defer ticker.Stop()
	staleStrikes := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !w.marketOpen() {
				staleStrikes = 0
				continue
			}
			w.lastEvent6Mu.Lock()
			since := time.Since(w.lastEvent6)
			w.lastEvent6Mu.Unlock()
			if since > wsGuardStale {
				staleStrikes++
				if staleStrikes >= 2 {
					w.healthy.Store(false)
					log.Printf("[cmc-ws] freshness guard: no type6 events > window, healthy=false → REST fallback")
				}
			} else {
				staleStrikes = 0
			}
		}
	}
}

func (w *WsClient) runOnce(ctx context.Context) error {
	jwt, err := w.auth.JWT(ctx)
	if err != nil {
		return fmt.Errorf("jwt: %w", err)
	}
	wsURL := strings.Replace(w.wsBase, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	// 1. auth（等 ack {"auth":{"status":"ok"}}，超时 10s 失败重连）
	authMsg, _ := json.Marshal(map[string]any{"auth": map[string]any{"token": jwt}})
	if err := w.writeMsg(conn, authMsg); err != nil {
		return fmt.Errorf("auth write: %w", err)
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, ackData, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("auth ack read: %w", err)
	}
	var ackEnv wsEnvelope
	if err := json.Unmarshal(ackData, &ackEnv); err != nil || ackEnv.Auth == nil {
		return fmt.Errorf("auth ack parse: %w (first frame not auth ack)", err)
	}
	if ackEnv.Auth.Status != "ok" {
		return fmt.Errorf("auth ack status=%s", ackEnv.Auth.Status)
	}
	// 2. D-225 v2.1：取消 REST 播种——type7 首帧全量快照=种子（F3）；5s 无首帧 → REST 兜底一次。
	w.seedDone.Store(false)
	// 3. subscribe 5/6/7
	for _, t := range []int{5, 6, 7} {
		msg, _ := json.Marshal(map[string]any{
			"subscribe": map[string]any{"subscriptionId": fmt.Sprintf("%d", w.accountID), "type": t},
		})
		if err := w.writeMsg(conn, msg); err != nil {
			return fmt.Errorf("subscribe type %d: %w", t, err)
		}
	}
	log.Printf("[cmc-ws] connected+subscribed account=%d", w.accountID)
	// 4. heartbeat goroutine
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(wsHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				hb, _ := json.Marshal(map[string]any{"heartbeat": map[string]any{}})
				if err := w.writeMsg(conn, hb); err != nil {
					return
				}
			}
		}
	}()
	// 5. D-225 v2.1：5s 首帧兜底定时器
	safego.Go("cmc-ws:seedFallback", func() {
		select {
		case <-done:
			return
		case <-time.After(wsSeedFallbackDelay):
			if !w.seedDone.Load() {
				log.Printf("[cmc-ws] no type7 snapshot in 5s, REST seed fallback")
				w.restSeedPositions(ctx, jwt)
			}
		}
	})
	// 6. 价格心跳定时器（4min）
	w.startPriceHeartbeat(ctx, done)
	// 7. read loop
	w.healthy.Store(true)
	w.markEvent6()
	for {
		if ctx.Err() != nil {
			return nil
		}
		conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		_, data, err := conn.ReadMessage()
		if err != nil {
			w.healthy.Store(false)
			return fmt.Errorf("read: %w", err)
		}
		w.handleMessage(ctx, data)
	}
}

// restSeedPositions REST 播种兜底（5s 无首帧或 patch 异常时调用，复用 fetchPositionsJSON）。
func (w *WsClient) restSeedPositions(ctx context.Context, jwt string) {
	seedPos, err := fetchPositionsJSON(ctx, jwt, w.restBase, w.accountID)
	if err != nil {
		log.Printf("[cmc-ws] REST seed fallback failed: %v", err)
		return
	}
	w.positionsMu.Lock()
	w.positions = seedPos.Positions
	w.positionsMu.Unlock()
	w.seedDone.Store(true)
	w.scheduleFlush()
}

func (w *WsClient) startPriceHeartbeat(ctx context.Context, done <-chan struct{}) {
	w.priceMu.Lock()
	if w.priceHeartbeat != nil {
		w.priceHeartbeat.Stop()
	}
	w.priceHeartbeat = time.AfterFunc(wsPriceHeartbeat, func() {
		select {
		case <-done:
			return
		default:
		}
		if ctx.Err() != nil {
			return
		}
		w.persistPriceHeartbeat(ctx)
		w.startPriceHeartbeat(ctx, done) // 重置下一轮
	})
	w.priceMu.Unlock()
}

func (w *WsClient) writeMsg(conn *websocket.Conn, msg []byte) error {
	w.connMu.Lock()
	defer w.connMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, msg)
}

func (w *WsClient) markEvent6() {
	w.lastEvent6Mu.Lock()
	w.lastEvent6 = time.Now()
	w.lastEvent6Mu.Unlock()
	w.healthy.Store(true)
}

func (w *WsClient) handleMessage(ctx context.Context, data []byte) {
	var env wsEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}
	if env.Auth != nil {
		return // auth ack 由 runOnce 处理
	}
	if env.Init != nil {
		log.Printf("[cmc-ws] init: heartbeatInterval=%dms heartbeatTimeout=%dms subscriptions=%d",
			env.Init.HeartbeatInterval, env.Init.HeartbeatTimeout, env.Init.SubscriptionsCount)
		return
	}
	if env.Subscribe != nil {
		if env.Subscribe.Status != "ok" {
			log.Printf("[cmc-ws] subscribe ack status=%s (非 ok)", env.Subscribe.Status)
		}
		return
	}
	if env.Heartbeat != nil {
		if env.Heartbeat.Status != "ok" {
			log.Printf("[cmc-ws] heartbeat ack status=%s (非 ok)", env.Heartbeat.Status)
		}
		return
	}
	if env.Msg == nil {
		return
	}
	msg := env.Msg
	switch msg.Type {
	case 5:
		w.handleType5(ctx, msg.Data)
	case 6:
		w.handleType6(ctx, msg.Data)
		w.markEvent6() // D-225 v2.1：守卫只看 type6
	case 7:
		w.handleType7(ctx, msg)
	default:
		return
	}
}

func (w *WsClient) kickHistoryDebounced() {
	if w.kickHistory == nil {
		return
	}
	w.kickMu.Lock()
	defer w.kickMu.Unlock()
	if time.Since(w.lastKickTime) >= wsKickDebounce {
		w.lastKickTime = time.Now()
		go w.kickHistory()
	}
}
