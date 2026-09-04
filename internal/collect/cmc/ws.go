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
	"arb/internal/store/pgstore"

	"github.com/gorilla/websocket"
)

const DefaultWsBase = "wss://connectapi.cmcmarkets.com/ws"

const (
	wsHeartbeatInterval = 5 * time.Second
	wsReadTimeout       = 12 * time.Second
	wsType6Throttle     = 60 * time.Second
	wsType6MinDelta     = 0.01
	wsPatchBurstWindow  = 2 * time.Second
	wsKickDebounce      = 30 * time.Second
)

var wsBackoff = func(attempt int) time.Duration { // 指数退避 1s→60s 封顶（D-209 模式，测试覆写）
	d := time.Duration(1<<uint(attempt)) * time.Second
	if d > 60*time.Second {
		return 60 * time.Second
	}
	return d
}

// WsClient CMC WebSocket 事件驱动采集器。
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
	// 1. auth（不等待 ack——安全评估 §2 无 ack 样本，以首条推送/心跳响应为有效性信号）
	authMsg, _ := json.Marshal(map[string]any{"auth": map[string]any{"token": jwt}})
	if err := w.writeMsg(conn, authMsg); err != nil {
		return fmt.Errorf("auth write: %w", err)
	}
	// 2. 播种（D-223 注记①：subscribe 前用 REST 拉全量 positions 为 patch 基底）
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
	// 5. read loop
	w.healthy.Store(true)
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
}
func (w *WsClient) handleMessage(ctx context.Context, data []byte) {
	var msg wsMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("[cmc-ws] unmarshal: %v", err)
		return
	}
	switch msg.Type {
	case 5:
		w.handleType5(ctx, msg.Data)
	case 6:
		w.handleType6(ctx, msg.Data)
	case 7:
		w.handleType7(ctx, msg.Partial)
	default:
		return // type=0：心跳响应/auth ack/未知 → 忽略
	}
	w.markEvent()
}

func (w *WsClient) handleType5(ctx context.Context, data json.RawMessage) {
	var s accountSummaryJSON
	if err := json.Unmarshal(data, &s); err != nil {
		log.Printf("[cmc-ws] type5 unmarshal: %v", err)
		return
	}
	w.lastSummaryMu.Lock()
	w.lastSummary = s
	w.lastSummaryMu.Unlock()
	w.persistSummary(ctx, &s)
}
func (w *WsClient) handleType6(ctx context.Context, data json.RawMessage) {
	var v struct {
		TotalAccountValue string `json:"totalAccountValue"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		log.Printf("[cmc-ws] type6 unmarshal: %v", err)
		return
	}
	val := parseFloat(v.TotalAccountValue)
	w.lastSummaryMu.Lock()
	delta := val - w.lastType6Val
	shouldPersist := time.Since(w.lastSummaryPersist) >= wsType6Throttle &&
		(delta > wsType6MinDelta || delta < -wsType6MinDelta)
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
	w.emitFact(ctx, []fact.Fact{{
		Kind: fact.KindCMCAccount, Venue: "cmc", Symbol: "summary",
		Value: parseFloat(s.TotalAccountValue), Unit: fact.UnitUSD, Ts: now, Src: "cmc_ws",
	}})
}
func (w *WsClient) handleType7(ctx context.Context, patches []wsPatch) {
	w.positionsMu.Lock()
	for _, patch := range patches {
		w.applyPatch(patch)
	}
	w.positionsMu.Unlock()
	if w.shouldKickHistory(patches) {
		w.kickHistoryDebounced()
	}
	w.scheduleFlush()
}
func (w *WsClient) applyPatch(patch wsPatch) {
	if patch.T != "m" {
		log.Printf("[cmc-ws] unknown patch type: %s", patch.T)
		return
	}
	parts := strings.Split(strings.TrimPrefix(patch.P, "/"), "/")
	if len(parts) < 2 || parts[0] != "positions" {
		log.Printf("[cmc-ws] unknown patch path: %s", patch.P)
		return
	}
	idx, err := strconv.Atoi(parts[1])
	if err != nil || idx < 0 || idx >= len(w.positions) {
		log.Printf("[cmc-ws] patch position index out of range: %s", patch.P)
		return
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(patch.D, &d); err != nil {
		log.Printf("[cmc-ws] patch data unmarshal: %v", err)
		return
	}
	pos := &w.positions[idx]
	fields := map[string]*string{
		"instrumentId":                   &pos.InstrumentID,
		"direction":                      &pos.Direction,
		"quantity":                       &pos.Quantity,
		"price":                          &pos.Price,
		"standardMarginAmount":           &pos.StandardMarginAmount,
		"marginAmount":                   &pos.MarginAmount,
		"standardMarginPercentage":       &pos.StandardMarginPercentage,
		"unrealisedPnLInPrimaryCurrency": &pos.UnrealisedPnLInPrimaryCurrency,
		"evaluationBidPrice":             &pos.EvaluationBidPrice,
		"evaluationAskPrice":             &pos.EvaluationAskPrice,
	}
	for k, v := range d {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			continue // 非字符串值跳过（positionJSON 字段全为 string）
		}
		if ptr, ok := fields[k]; ok {
			*ptr = s
		} else {
			log.Printf("[cmc-ws] unknown patch field: %s", k)
		}
	}
}
func (w *WsClient) shouldKickHistory(patches []wsPatch) bool {
	for _, p := range patches {
		if strings.Contains(p.P, "/orders") {
			return true
		}
	}
	return false
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
	w.flushTimer = time.AfterFunc(wsPatchBurstWindow, func() { w.flushPositions() })
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
			EvaluationBidPrice: parseFloat(p.EvaluationBidPrice), EvaluationAskPrice: parseFloat(p.EvaluationAskPrice),
			FetchedAt: now,
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
	w.emitFact(context.Background(), []fact.Fact{{
		Kind: fact.KindCMCAccount, Venue: "cmc", Symbol: "positions",
		Value: 0, Unit: fact.UnitUSD, Ts: now, Src: "cmc_ws",
	}})
}
func (w *WsClient) emitFact(ctx context.Context, facts []fact.Fact) {
	if w.sink != nil {
		if err := w.sink(ctx, facts); err != nil {
			log.Printf("[cmc-ws] sink: %v", err)
		}
	}
}

type wsMessage struct {
	Type      int             `json:"type"`
	Data      json.RawMessage `json:"data"`
	Partial   []wsPatch       `json:"partial"`
	Heartbeat json.RawMessage `json:"heartbeat"`
}
type wsPatch struct {
	T string          `json:"t"` // "m" = modify
	P string          `json:"p"` // path like "/positions/0/orders/0"
	D json.RawMessage `json:"d"` // data to merge
}
