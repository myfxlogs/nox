// CMC WebSocket 事件驱动采集（Phase A，D-223 设计 C-Final）。
// WsClient 维持 wss://connectapi.cmcmarkets.com/ws 长连接，订阅 type 5/6/7，
// 事件落库 + fact 发射（Src="cmc_ws"）；断连自动重连+重订阅。
// AccountCollector.Poll 在 ws 健康时短路（REST 兜底降级）。
package cmc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
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
	wsType6Throttle     = 60 * time.Second
	wsType6MinDelta     = 0.01
	wsPatchBurstWindow  = 2 * time.Second
	wsKickDebounce      = 30 * time.Second
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

type WsClient struct {
	auth               jwtProvider
	accountID          int64
	store              *pgstore.Store
	wsBase             string
	restBase           string
	healthy            atomic.Bool
	kickHistory        func()
	sink               func(ctx context.Context, facts []fact.Fact) error
	marketOpen         func() bool
	positionsMu        sync.Mutex
	positions          []positionJSON
	lastSummaryMu      sync.Mutex
	lastSummary        accountSummaryJSON
	lastSummaryPersist time.Time
	lastType6Val       float64
	lastEventMu        sync.Mutex
	lastEvent          time.Time
	flushMu            sync.Mutex
	flushTimer         *time.Timer
	kickMu             sync.Mutex
	lastKickTime       time.Time
	connMu             sync.Mutex
}

func NewWsClient(auth jwtProvider, accountID int64, store *pgstore.Store, wsBase, restBase string) *WsClient {
	return &WsClient{
		auth: auth, accountID: accountID, store: store, wsBase: wsBase, restBase: restBase,
		marketOpen: func() bool { return true }, // 保守默认：无缓存时视为市场时段
	}
}

func (w *WsClient) SetKickHistory(fn func())                                      { w.kickHistory = fn }
func (w *WsClient) SetSink(fn func(ctx context.Context, facts []fact.Fact) error) { w.sink = fn }
func (w *WsClient) SetMarketOpen(fn func() bool) {
	if fn != nil {
		w.marketOpen = fn
	}
}
func (w *WsClient) IsHealthy() bool { return w.healthy.Load() }
func (w *WsClient) Run(ctx context.Context) {
	safego.Go("cmc-ws:guard", func() { w.startGuard(ctx) }) // C2 路径② 新鲜度守卫
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
func (w *WsClient) startGuard(ctx context.Context) { // C2 路径② 新鲜度守卫：连续 2 窗无事件 → 置 false（防抖）
	ticker := time.NewTicker(wsGuardInterval)
	defer ticker.Stop()
	staleStrikes := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !w.marketOpen() { // 休市跳过——无事件是正常的
				staleStrikes = 0
				continue
			}
			w.lastEventMu.Lock()
			since := time.Since(w.lastEvent)
			w.lastEventMu.Unlock()
			if since > wsGuardStale {
				staleStrikes++
				if staleStrikes >= 2 {
					w.healthy.Store(false) // 路径②：哑连击穿（连接活但无事件）
					// D-193-A 扩包返修 R4：guard 翻转打一行（每次翻转一条）
					log.Printf("[cmc-ws] freshness guard: no events > window, healthy=false → REST fallback")
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
	// 1. auth（D-193-A/R6 返修：等 auth ack {"auth":{"status":"ok"}}，超时 10s 失败重连）
	// D-193-A/R6 S3：JWT 会话过期（5min 1011 断连）→ 1s 重连自带新 JWT（已实证可用循环）；
	// 本包不做连接内刷新（协议未取证），重连即续命，Phase B 可选优化。
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
	// 2. 播种（subscribe 前用 REST 拉全量 positions 为 patch 基底）
	seedPos, err := fetchPositionsJSON(ctx, jwt, w.restBase, w.accountID)
	if err != nil {
		return fmt.Errorf("seed positions: %w", err)
	}
	w.positionsMu.Lock()
	w.positions = seedPos.Positions
	w.positionsMu.Unlock()
	// 3. subscribe 5/6/7
	for _, t := range []int{5, 6, 7} {
		msg, _ := json.Marshal(map[string]any{
			"subscribe": map[string]any{"subscriptionId": fmt.Sprintf("%d", w.accountID), "type": t},
		})
		if err := w.writeMsg(conn, msg); err != nil {
			return fmt.Errorf("subscribe type %d: %w", t, err)
		}
	}
	// D-193-A 扩包返修 R4：订阅成功后打一行（每次连接一条，勿每事件打）
	log.Printf("[cmc-ws] connected+subscribed account=%d", w.accountID)
	// 4. heartbeat goroutine（5s 发送，读超时 12s → 断连进重连）
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
	w.healthy.Store(true) // 5. read loop
	w.markEvent()
	for {
		if ctx.Err() != nil {
			return nil
		}
		conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		_, data, err := conn.ReadMessage()
		if err != nil {
			w.healthy.Store(false) // 路径①：读循环错误/心跳 12s 超时（断连）
			return fmt.Errorf("read: %w", err)
		}
		w.handleMessage(ctx, data)
	}
}
func (w *WsClient) writeMsg(conn *websocket.Conn, msg []byte) error {
	w.connMu.Lock()
	defer w.connMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, msg)
}
func (w *WsClient) markEvent() {
	w.lastEventMu.Lock()
	w.lastEvent = time.Now()
	w.lastEventMu.Unlock()
	w.healthy.Store(true)
}
func (w *WsClient) handleMessage(ctx context.Context, data []byte) {
	// D-193-A/R6 返修：解信封 {"<kind>":{...}}，只 msg 进事件分发
	var env wsEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}
	// 控制帧按需 log（auth/subscribe 失败才 log，成功静默）
	if env.Auth != nil {
		// auth ack 由 runOnce 等待循环处理，此处不重复（runOnce 读首帧判 auth）
		return
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
	// msg 事件帧（5/6/7）
	if env.Msg == nil {
		return
	}
	msg := env.Msg
	switch msg.Type {
	case 5:
		var s accountSummaryJSON
		if err := json.Unmarshal(msg.Data, &s); err == nil {
			w.lastSummaryMu.Lock()
			w.lastSummary = s
			w.lastSummaryMu.Unlock()
			w.persistSummary(ctx, &s)
		}
	case 6:
		w.handleType6(ctx, msg.Data)
	case 7:
		w.handleType7(ctx, msg.Partial)
	default:
		return
	}
	w.markEvent()
}
func (w *WsClient) handleType6(ctx context.Context, data json.RawMessage) {
	var v struct {
		TotalAccountValue string `json:"totalAccountValue"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return
	}
	w.lastSummaryMu.Lock()
	delta := parseFloat(v.TotalAccountValue) - w.lastType6Val
	shouldPersist := time.Since(w.lastSummaryPersist) >= wsType6Throttle && (delta > wsType6MinDelta || delta < -wsType6MinDelta)
	s := w.lastSummary
	s.TotalAccountValue = v.TotalAccountValue
	w.lastSummaryMu.Unlock()
	if shouldPersist {
		w.persistSummary(ctx, &s)
	}
}
func (w *WsClient) persistSummary(ctx context.Context, s *accountSummaryJSON) {
	cash := parseFloat(s.TotalCashBalance)
	w.positionsMu.Lock()
	posCopy := make([]positionJSON, len(w.positions))
	copy(posCopy, w.positions)
	w.positionsMu.Unlock()
	mid, _ := w.store.LastCMCPriceMid(ctx, "X-AFQUL")
	closeout, distance := calcCloseoutValues(cash, &positionsJSON{Positions: posCopy}, mid)
	now := time.Now()
	rec := pgstore.CMCAccountSummary{
		AccountID: w.accountID, TotalAccountValue: parseFloat(s.TotalAccountValue),
		TotalCashBalance: cash, TotalUnrealisedPnL: parseFloat(s.TotalUnrealisedPnL),
		TotalEquityFreeForMargin: parseFloat(s.TotalEquityFreeForMargin),
		TotalEquityUsedInMargin:  parseFloat(s.TotalEquityUsedInMargin),
		CloseoutPrice:            closeout, DistanceToCloseoutPct: distance, FetchedAt: now,
	}
	if err := w.store.InsertCMCAccountSummary(ctx, rec); err != nil {
		log.Printf("[cmc-ws] insert summary: %v", err)
		return
	}
	w.lastSummaryMu.Lock()
	w.lastSummaryPersist = now
	w.lastType6Val = parseFloat(s.TotalAccountValue)
	w.lastSummaryMu.Unlock()
	w.emitFact(ctx, []fact.Fact{{Kind: fact.KindCMCAccount, Venue: "cmc", Symbol: "summary",
		Value: parseFloat(s.TotalAccountValue), Unit: fact.UnitUSD, Ts: now, Src: "cmc_ws"}})
}
func (w *WsClient) handleType7(ctx context.Context, patches []wsPatch) {
	w.positionsMu.Lock()
	for _, patch := range patches {
		w.applyPatch(patch)
	}
	w.positionsMu.Unlock()
	for _, p := range patches {
		if strings.Contains(p.P, "/orders") {
			w.kickHistoryDebounced()
			break
		}
	}
	w.scheduleFlush()
}
func (w *WsClient) applyPatch(patch wsPatch) {
	if patch.T != "m" {
		return
	}
	parts := strings.Split(strings.TrimPrefix(patch.P, "/"), "/")
	if len(parts) < 2 || parts[0] != "positions" {
		return
	}
	idx, err := strconv.Atoi(parts[1])
	if err != nil || idx < 0 || idx >= len(w.positions) {
		return
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(patch.D, &d); err != nil {
		return
	}
	pos := &w.positions[idx]
	fields := map[string]*string{
		"instrumentId": &pos.InstrumentID, "direction": &pos.Direction, "quantity": &pos.Quantity,
		"price": &pos.Price, "standardMarginAmount": &pos.StandardMarginAmount, "marginAmount": &pos.MarginAmount,
		"standardMarginPercentage":       &pos.StandardMarginPercentage,
		"unrealisedPnLInPrimaryCurrency": &pos.UnrealisedPnLInPrimaryCurrency,
		"evaluationBidPrice":             &pos.EvaluationBidPrice,
		"evaluationAskPrice":             &pos.EvaluationAskPrice,
	}
	for k, v := range d {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			continue
		}
		if ptr, ok := fields[k]; ok {
			*ptr = s
		}
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
func (w *WsClient) scheduleFlush() {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()
	if w.flushTimer != nil {
		w.flushTimer.Stop()
	}
	w.flushTimer = time.AfterFunc(wsPatchBurstWindow, w.flushPositions)
}
func (w *WsClient) flushPositions() {
	w.positionsMu.Lock()
	posCopy := make([]positionJSON, len(w.positions))
	copy(posCopy, w.positions)
	w.positionsMu.Unlock()
	if len(posCopy) == 0 {
		return
	}
	now := time.Now()
	dbPositions := make([]pgstore.CMCPosition, 0, len(posCopy))
	for _, p := range posCopy {
		dp := pgstore.CMCPosition{
			AccountID: w.accountID, InstrumentID: p.InstrumentID, Direction: p.Direction,
			Quantity: parseFloat(p.Quantity), EntryPrice: parseFloat(p.Price),
			UnrealisedPnL: parseFloat(p.UnrealisedPnLInPrimaryCurrency),
			MarginAmount:  parseFloat(p.MarginAmount), MarginPercentage: parseFloat(p.StandardMarginPercentage),
			EvaluationBidPrice: parseFloat(p.EvaluationBidPrice),
			EvaluationAskPrice: parseFloat(p.EvaluationAskPrice), FetchedAt: now,
		}
		if len(p.Orders) > 0 {
			dp.OrderID = p.Orders[0].OrderID
			if t, err := parseRFC3339Loose(p.Orders[0].SubmittedTimestamp); err == nil {
				dp.OpenedAt = &t
			}
		}
		dbPositions = append(dbPositions, dp)
	}
	if err := w.store.InsertCMCPositions(context.Background(), w.accountID, dbPositions); err != nil {
		log.Printf("[cmc-ws] insert positions: %v", err)
		return
	}
	w.emitFact(context.Background(), []fact.Fact{{Kind: fact.KindCMCAccount, Venue: "cmc",
		Symbol: "positions", Value: 0, Unit: fact.UnitUSD, Ts: now, Src: "cmc_ws"}})
}
func (w *WsClient) emitFact(ctx context.Context, facts []fact.Fact) {
	if w.sink != nil {
		if err := w.sink(ctx, facts); err != nil {
			log.Printf("[cmc-ws] sink: %v", err)
		}
	}
}

