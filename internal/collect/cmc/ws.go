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
	"log"
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
	relogin      func(context.Context) error // D-370 S1：Session expired → 完整重登缝
	marketStatus func() string               // D-225 v2.1 S5：WS 价格行 market_status 注入
	onEvent      func(Snapshot)              // D-225 v2.1 S5：事件即触发回调（R10 接线）
	frameHook    func(FrameKind)             // D-234：帧钩子——落库后通知 dashboard（先落库后通知，通知失败不影响落库）

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
	flushMu    sync.Mutex
	flushTimer *time.Timer

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
	w := &WsClient{
		auth: auth, accountID: accountID, store: store, wsBase: wsBase, restBase: restBase,
		marketStatus: func() string { return "" }, // 默认无注入（R2 修复：S1 重构误删已补回）
		lastAlerts:   json.RawMessage("[]"),
	}
	// D-370 S1：auth 实现 Login 时注入完整重登缝（Session expired → Login 而非 refreshJWT）。
	if p, ok := auth.(interface {
		Login(context.Context) error
	}); ok {
		w.relogin = p.Login
	}
	return w
}

func (w *WsClient) SetKickHistory(fn func())                                      { w.kickHistory = fn }
func (w *WsClient) SetSink(fn func(ctx context.Context, facts []fact.Fact) error) { w.sink = fn }
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
			// D-370 S1：Session expired → 完整 re-Login（4 步全链，不等 refreshJWT 阈值）；
			// 失败仍走既有退避（不重置 attempt）。
			w.maybeRelogin(ctx, err)
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

// runOnce + handleMessage 见 ws_run.go（从 ws.go 拆出保 ≤300 行）。

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
