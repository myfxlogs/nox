package cmc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"

	"github.com/gorilla/websocket"
)

// wsTestServer fake CMC WS+REST 服务端（httptest + gorilla upgrade）。
type wsTestServer struct {
	t        *testing.T
	srv      *httptest.Server
	upgrader websocket.Upgrader

	mu             sync.Mutex
	subscribeCount int           // 累计 subscribe 消息数
	connCount      int           // 累计连接数
	subCh          chan struct{} // 每次三条 subscribe 到齐后关闭
	closeOnNext    bool          // 下次连接后立即关闭（T2 重连测试）
	positionsJSON  string        // REST 播种返回的 positions JSON
	connectedConns []*websocket.Conn
}

func newWsTestServer(t *testing.T, positionsJSON string) *wsTestServer {
	ts := &wsTestServer{
		t:             t,
		upgrader:      websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		subCh:         make(chan struct{}, 4),
		positionsJSON: positionsJSON,
	}
	ts.srv = httptest.NewServer(http.HandlerFunc(ts.handler))
	return ts
}

func (ts *wsTestServer) close() { ts.srv.Close() }

func (ts *wsTestServer) handler(w http.ResponseWriter, r *http.Request) {
	// REST positions 端点（播种）
	if strings.Contains(r.URL.Path, "/positions") && r.Header.Get("Upgrade") == "" {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(ts.positionsJSON))
		return
	}
	// WebSocket upgrade
	conn, err := ts.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ts.mu.Lock()
	ts.connCount++
	ts.connectedConns = append(ts.connectedConns, conn)
	closeNow := ts.closeOnNext
	ts.mu.Unlock()

	if closeNow {
		_ = conn.Close()
		return
	}
	defer conn.Close()

	// 读取客户端消息（auth + subscribe x3 + heartbeat），统计 subscribe
	// D-193-A/R6 返修：收到 auth 后发 auth ack + init 帧（对照取证日志 FRAME[1]/[2]）
	subscribeSeen := 0
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if _, ok := msg["auth"]; ok {
			// 发 auth ack + init 帧（runOnce 等 auth ack 才进 subscribe）
			_ = conn.WriteMessage(websocket.TextMessage, []byte(wsAuthAck))
			_ = conn.WriteMessage(websocket.TextMessage, []byte(wsInitFrame))
			continue
		}
		if _, ok := msg["subscribe"]; ok {
			ts.mu.Lock()
			ts.subscribeCount++
			subscribeSeen++
			if subscribeSeen == 3 {
				ts.subCh <- struct{}{}
			}
			ts.mu.Unlock()
		}
	}
}

// sendEvent 通过指定连接推送一条 ws 事件。
func (ts *wsTestServer) sendEvent(connIdx int, event string) {
	ts.mu.Lock()
	if connIdx >= len(ts.connectedConns) {
		ts.mu.Unlock()
		return
	}
	conn := ts.connectedConns[connIdx]
	ts.mu.Unlock()
	_ = conn.WriteMessage(websocket.TextMessage, []byte(event))
}

// wsType5Event D-193-A/R6 返修：信封版 {"msg":{type:5,...}}（对照取证日志 FRAME[3]）。
const wsType5Event = `{
  "msg": {
    "type": 5,
    "subscriptionId": "38941616",
    "data": {
      "primaryAccountCurrency": "USD",
      "totalAccountValue": "2185.31",
      "totalEquityFreeForCashTrading": "1442.67",
      "totalEquityFreeForMarginTrading": "1601.22",
      "totalEquityUsedInMarginTrading": "584.09",
      "totalCashBalance": "2026.76",
      "totalUnrealisedPnL": "158.56"
    }
  }
}`

// wsType6Event D-193-A/R6 返修：信封版 {"msg":{type:6,...}}（对照取证日志 FRAME[7]）。
const wsType6Event = `{
  "msg": {
    "type": 6,
    "subscriptionId": "38941616",
    "data": {
      "timestamp": "2026-09-02T08:03:45.0000000Z",
      "primaryAccountCurrency": "USD",
      "totalAccountValue": "2186.00",
      "totalMidPriceAccountValue": "2189.84",
      "totalEquityFreeForCashTrading": "1442.67",
      "totalEquityFreeForMarginTrading": "1601.22"
    }
  }
}`

// wsType7Event D-193-A/R6 返修：信封版 {"msg":{type:7,...}}（对照取证日志 FRAME[19]）。
const wsType7Event = `{
  "msg": {
    "type": 7,
    "subscriptionId": "38941616",
    "data": null,
    "partial": [{
      "t": "m",
      "p": "/positions/0/orders/0",
      "d": {
        "unrealisedPnLInInstrumentCurrency": "156.93",
        "unrealisedPnLInPrimaryCurrency": "156.93",
        "closeValueInInstrumentCurrency": "6160.88",
        "closeValueInAccountCurrency": "6160.88"
      }
    }]
  }
}`

// wsAuthAck D-193-A/R6 返修：auth ack 信封 {"auth":{"status":"ok"}}（对照取证日志 FRAME[1]）。
const wsAuthAck = `{"auth":{"status":"ok"}}`

// wsInitFrame D-193-A/R6 返修：init 信封 {"init":{heartbeatInterval,...}}（对照取证日志 FRAME[2]）。
const wsInitFrame = `{"init":{"heartbeatInterval":5000,"heartbeatTimeout":12000,"subscriptionsCount":0}}`

// waitSubscribed 等待三条 subscribe 到齐（带超时）。
func (ts *wsTestServer) waitSubscribed(timeout time.Duration) bool {
	select {
	case <-ts.subCh:
		return true
	case <-time.After(timeout):
		return false
	}
}

// ---------- T1 全链测试 ----------

func TestWsClient_FullChain(t *testing.T) {
	st := newTestStore(t)
	// D-225 v2.1：先写入一条 price（含 ask——closeout 计算走 ForSide）
	_ = st.InsertCMCPrice(context.Background(), pgstorePriceRec("X-AFQUL", 0.324530))

	ts := newWsTestServer(t, mockPositionsJSON)
	defer ts.close()
	auth := &mockAuth{jwt: "fake.jwt.sig"}
	ws := NewWsClient(auth, 38941616, st, ts.srv.URL, ts.srv.URL)

	// sink 捕获 fact
	var factMu sync.Mutex
	var capturedFacts []fact.Fact
	ws.SetSink(func(_ context.Context, fs []fact.Fact) error {
		factMu.Lock()
		capturedFacts = append(capturedFacts, fs...)
		factMu.Unlock()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ws.Run(ctx)

	// 等待 subscribe 三条
	if !ts.waitSubscribed(10 * time.Second) {
		t.Fatal("timeout waiting for subscribe")
	}
	// D-225 v2.1：type6 是账户主数据源（落库）；type5 降为交叉校验（不落库）
	ts.sendEvent(0, wsType6Event)
	// 推 type=7（持仓 patch）
	ts.sendEvent(0, wsType7Event)

	// 等待落库（flush 15s 节流 + DB 写入）
	time.Sleep(16 * time.Second)

	// 断言 summary 落表（type6 驱动）
	var summaryCount int
	_ = st.Pool().QueryRow(context.Background(),
		"SELECT count(*) FROM fin_cmc_account_summary WHERE account_id = 38941616").Scan(&summaryCount)
	if summaryCount == 0 {
		t.Error("no summary rows written from type=6 event")
	}

	// 断言 positions 落表（type=7 patch 后 flush）
	var posCount int
	_ = st.Pool().QueryRow(context.Background(),
		"SELECT count(*) FROM fin_cmc_positions WHERE account_id = 38941616").Scan(&posCount)
	if posCount == 0 {
		t.Error("no positions rows written from type=7 patch")
	}

	// 断言 fact 发射 Src="cmc_ws"
	factMu.Lock()
	hasWSFact := false
	for _, f := range capturedFacts {
		if f.Src == "cmc_ws" {
			hasWSFact = true
			break
		}
	}
	factMu.Unlock()
	if !hasWSFact {
		t.Error("no fact with Src=cmc_ws emitted")
	}

	// 断言三条 subscribe 发出
	ts.mu.Lock()
	subCount := ts.subscribeCount
	ts.mu.Unlock()
	if subCount < 3 {
		t.Errorf("subscribe count = %d, want >= 3", subCount)
	}
}

// ---------- T2 断连重连测试 ----------

func TestWsClient_Reconnect(t *testing.T) {
	st := newTestStore(t)
	ts := newWsTestServer(t, mockPositionsJSON)
	defer ts.close()

	// 缩短退避使重连快速发生
	origBackoff := wsBackoff
	wsBackoff = func(attempt int) time.Duration { return 100 * time.Millisecond }
	defer func() { wsBackoff = origBackoff }()

	auth := &mockAuth{jwt: "fake.jwt.sig"}
	ws := NewWsClient(auth, 38941616, st, ts.srv.URL, ts.srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ws.Run(ctx)

	// 第一次连接 subscribe 到齐
	if !ts.waitSubscribed(10 * time.Second) {
		t.Fatal("timeout waiting for first subscribe")
	}

	// 关闭第一条连接，触发重连
	ts.mu.Lock()
	if len(ts.connectedConns) > 0 {
		_ = ts.connectedConns[0].Close()
	}
	ts.mu.Unlock()

	// 等待重连后第二次 subscribe 到齐
	if !ts.waitSubscribed(10 * time.Second) {
		t.Fatal("timeout waiting for reconnect subscribe")
	}

	// 断言至少 6 条 subscribe（第一次 3 + 重连后 3）
	ts.mu.Lock()
	subCount := ts.subscribeCount
	connCount := ts.connCount
	ts.mu.Unlock()
	if subCount < 6 {
		t.Errorf("subscribe count = %d, want >= 6 (3 initial + 3 reconnect)", subCount)
	}
	if connCount < 2 {
		t.Errorf("connection count = %d, want >= 2", connCount)
	}
}

// pgstorePriceRec 辅助构造 CMCPrice 记录。
func pgstorePriceRec(instrumentID string, mid float64) pgstore.CMCPrice {
	return pgstore.CMCPrice{
		InstrumentID: instrumentID,
		MidPrice:     mid,
		FetchedAt:    time.Now(),
	}
}

// ---------- T3 哑连击穿（新鲜度守卫）测试 ----------

func TestWsClient_GuardDummyConnection(t *testing.T) {
	st := newTestStore(t)
	_ = st.InsertCMCPrice(context.Background(), pgstorePriceRec("X-AFQUL", 0.324530))

	ts := newWsTestServer(t, mockPositionsJSON)
	defer ts.close()
	auth := &mockAuth{jwt: "fake.jwt.sig"}
	ws := NewWsClient(auth, 38941616, st, ts.srv.URL, ts.srv.URL)
	ws.SetMarketOpen(func() bool { return true }) // 市场时段

	// 缩短守卫窗：interval=200ms，stale=500ms（2 个检查窗=400ms < 500ms → 第 2 窗触发）
	origInterval, origStale := wsGuardInterval, wsGuardStale
	wsGuardInterval = 200 * time.Millisecond
	wsGuardStale = 500 * time.Millisecond
	defer func() {
		wsGuardInterval = origInterval
		wsGuardStale = origStale
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ws.Run(ctx)

	if !ts.waitSubscribed(10 * time.Second) {
		t.Fatal("timeout waiting for subscribe")
	}

	// D-225 v2.1：守卫只看 type6 事件——推 type6 建立计时，type5 不再触发守卫
	ts.sendEvent(0, wsType6Event)
	time.Sleep(300 * time.Millisecond) // 等事件处理

	// 等待守卫击穿：2 个检查窗（~400ms）+ stale 500ms → ~900ms，给 3s 余量
	deadline := time.Now().Add(3 * time.Second)
	healthyBecameFalse := false
	for time.Now().Before(deadline) {
		if !ws.IsHealthy() {
			healthyBecameFalse = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !healthyBecameFalse {
		t.Fatal("guard did not trip healthy=false after stale window (dummy connection)")
	}

	// 重发 type6 事件 → 守卫恢复 healthy=true
	ts.sendEvent(0, wsType6Event)
	deadline = time.Now().Add(2 * time.Second)
	healthyBecameTrue := false
	for time.Now().Before(deadline) {
		if ws.IsHealthy() {
			healthyBecameTrue = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !healthyBecameTrue {
		t.Fatal("guard did not recover healthy=true after event resumed")
	}
}

// ---------- T4 节流与 patch 合并测试 ----------

func TestWsClient_ThrottleAndPatchBurst(t *testing.T) {
	st := newTestStore(t)
	_ = st.InsertCMCPrice(context.Background(), pgstorePriceRec("X-AFQUL", 0.324530))

	ts := newWsTestServer(t, mockPositionsJSON)
	defer ts.close()
	auth := &mockAuth{jwt: "fake.jwt.sig"}
	ws := NewWsClient(auth, 38941616, st, ts.srv.URL, ts.srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ws.Run(ctx)

	if !ts.waitSubscribed(10 * time.Second) {
		t.Fatal("timeout waiting for subscribe")
	}

	// T4a: 60s 内推 10 条 type=6 → 仅 1 行落库（节流 60s）
	for i := 0; i < 10; i++ {
		ts.sendEvent(0, wsType6Event)
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond) // 等落库

	var summaryCount int
	_ = st.Pool().QueryRow(context.Background(),
		"SELECT count(*) FROM fin_cmc_account_summary WHERE account_id = 38941616").Scan(&summaryCount)
	if summaryCount != 1 {
		t.Errorf("type=6 throttle: summary rows = %d, want 1 (10 events in 60s window)", summaryCount)
	}

	// T4b: type=7 burst 3 条 patch → 1 次 flush 落库且字段合并
	// D-225 v2.1：flush 节流从 2s 改 15s——3 条 patch 在 15s 窗口内合并为 1 次 flush
	// 先清 positions 表确保计数干净
	_, _ = st.Pool().Exec(context.Background(),
		"DELETE FROM fin_cmc_positions WHERE account_id = 38941616")

	// D-225 v2.1：先推 type7 首帧快照种子（patch 需要已有 positions 才能合并）
	ts.sendEvent(0, wsType7SnapshotEvent)
	time.Sleep(500 * time.Millisecond)

	// 3 条 position 级 patch（更新 unrealisedPnLInPrimaryCurrency）→ 15s 窗口内合并为 1 次 flush
	for i := 0; i < 3; i++ {
		ts.sendEvent(0, `{"msg":{"type":7,"subscriptionId":"38941616","data":null,"partial":[{"t":"m","p":"/positions/0","d":{"unrealisedPnLInPrimaryCurrency":"156.93"}}]}}`)
		time.Sleep(50 * time.Millisecond) // < 15s → 合并到同一次 flush
	}
	time.Sleep(16 * time.Second) // 等 flush 窗口（15s）+ DB 写入

	var posCount int
	_ = st.Pool().QueryRow(context.Background(),
		"SELECT count(*) FROM fin_cmc_positions WHERE account_id = 38941616").Scan(&posCount)
	if posCount != 1 {
		t.Errorf("type=7 burst: positions rows = %d, want 1 (3 patches merged in 15s window)", posCount)
	}

	// 断言 patch 字段已合并：unrealisedPnLInPrimaryCurrency 应为最后一条 patch 的值
	var pnl string
	_ = st.Pool().QueryRow(context.Background(),
		"SELECT unrealised_pnl FROM fin_cmc_positions WHERE account_id = 38941616").Scan(&pnl)
	if pnl != "156.93" {
		t.Errorf("patch field merge: unrealised_pnl = %q, want 156.93", pnl)
	}
}
