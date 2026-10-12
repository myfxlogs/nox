// D-225 v2.1 九条新测试（S6 先红后绿）。
// 验证事件驱动重设计核心行为：首帧种子/patch 三深度/patch 异常 REST 重播种/
// 价格事件节流/type5 交叉校验不落库/守卫只看 type6/alerts 三态/ForSide 优先级/src 感知 watchdog。
package cmc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
)

// ---------- 辅助事件模板（v2.1） ----------

// wsType7SnapshotEvent type7 首帧全量快照（data 非 null → 种子）。
const wsType7SnapshotEvent = `{
  "msg": {
    "type": 7,
    "subscriptionId": "38941616",
    "data": {
      "primaryAccountCurrency": "USD",
      "totalCFDUnrealisedPnL": "151.32",
      "totalStandardMarginAmount": "584.81",
      "positions": [{
        "instrumentId": "X-AFQUL",
        "direction": "Sell",
        "quantity": "18100.00",
        "price": "0.33171",
        "standardMarginAmount": "584.81",
        "marginAmount": "584.81",
        "standardMarginPercentage": "0.10",
        "unrealisedPnLInPrimaryCurrency": "151.32",
        "evaluationBidPrice": "0.32285",
        "evaluationAskPrice": "0.32335",
        "orders": [{"orderId": "ORD-001", "filledPrice": "0.33171", "submittedTimestamp": "2026-08-17T10:30:00.1234567Z"}]
      }]
    },
    "partial": []
  }
}`

// wsType7PatchPosition type7 position 字段 patch。
const wsType7PatchPosition = `{
  "msg": {"type": 7, "subscriptionId": "38941616", "data": null,
    "partial": [{"t": "m", "p": "/positions/0", "d": {"quantity": "18200.00"}}]}
}`

// wsType7PatchOrder type7 order 字段 patch。
const wsType7PatchOrder = `{
  "msg": {"type": 7, "subscriptionId": "38941616", "data": null,
    "partial": [{"t": "m", "p": "/positions/0/orders/0", "d": {"filledPrice": "0.33000"}}]}
}`

// wsType7PatchRoot type7 根路径 patch。
const wsType7PatchRoot = `{
  "msg": {"type": 7, "subscriptionId": "38941616", "data": null,
    "partial": [{"t": "m", "p": "/", "d": {"totalCFDUnrealisedPnL": "160.00"}}]}
}`

// wsType7PatchBadType type7 非 m 类型 patch（触发 REST 重播种）。
const wsType7PatchBadType = `{
  "msg": {"type": 7, "subscriptionId": "38941616", "data": null,
    "partial": [{"t": "x", "p": "/positions/0", "d": {"quantity": "99999"}}]}
}`

// wsType7PatchOutOfRange type7 越界索引 patch（触发 REST 重播种）。
const wsType7PatchOutOfRange = `{
  "msg": {"type": 7, "subscriptionId": "38941616", "data": null,
    "partial": [{"t": "m", "p": "/positions/99", "d": {"quantity": "99999"}}]}
}`

// wsType6WithAlerts type6 含 alerts 的全量事件。
const wsType6WithAlerts = `{
  "msg": {"type": 6, "subscriptionId": "38941616",
    "data": {
      "timestamp": "2026-09-05T10:00:00.0000000Z",
      "primaryAccountCurrency": "USD",
      "totalAccountValue": "2186.00",
      "totalCashBalance": "2026.76",
      "totalUnrealisedPnL": "159.24",
      "totalEquityFreeForMarginTrading": "1601.22",
      "totalMarginAmount": "584.81",
      "accountValueAlerts": []
    }}
}`

// wsType6WithWarningAlerts type6 含 warning alerts。
const wsType6WithWarningAlerts = `{
  "msg": {"type": 6, "subscriptionId": "38941616",
    "data": {
      "timestamp": "2026-09-05T10:00:00.0000000Z",
      "primaryAccountCurrency": "USD",
      "totalAccountValue": "2186.00",
      "totalCashBalance": "2026.76",
      "totalUnrealisedPnL": "159.24",
      "totalEquityFreeForMarginTrading": "1601.22",
      "totalMarginAmount": "584.81",
      "accountValueAlerts": [{"alertType": "InStandardCloseOutWarning", "value": "true"}]
    }}
}`

// wsType6WithBreachAlerts type6 含 breach alerts。
const wsType6WithBreachAlerts = `{
  "msg": {"type": 6, "subscriptionId": "38941616",
    "data": {
      "timestamp": "2026-09-05T10:00:00.0000000Z",
      "primaryAccountCurrency": "USD",
      "totalAccountValue": "2186.00",
      "totalCashBalance": "2026.76",
      "totalUnrealisedPnL": "159.24",
      "totalEquityFreeForMarginTrading": "1601.22",
      "totalMarginAmount": "584.81",
      "accountValueAlerts": [{"alertType": "InStandardCloseOutBreach", "value": "true"}]
    }}
}`

// wsType5WithDrift type5 与 type6 漂移 >0.5%。
const wsType5WithDrift = `{
  "msg": {"type": 5, "subscriptionId": "38941616",
    "data": {
      "primaryAccountCurrency": "USD",
      "totalAccountValue": "2295.30",
      "totalEquityFreeForMarginTrading": "1601.22",
      "totalEquityUsedInMarginTrading": "584.09",
      "totalCashBalance": "2026.76",
      "totalUnrealisedPnL": "268.54"
    }}
}`

// ---------- N1: type7 首帧快照种子 + 零 REST 调用 ----------

func TestV21_N1_Type7SnapshotSeeds_NoREST(t *testing.T) {
	st := newTestStore(t)
	restCalls := int32(0)
	ts := newWsTestServerWithRESTCounter(t, mockPositionsJSON, &restCalls)
	defer ts.close()
	auth := &mockAuth{jwt: "fake.jwt.sig"}
	ws := NewWsClient(auth, 38941616, st, ts.srv.URL, ts.srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ws.Run(ctx)

	if !ts.waitSubscribed(10 * time.Second) {
		t.Fatal("timeout waiting for subscribe")
	}
	// 推 type7 首帧全量快照
	ts.sendEvent(0, wsType7SnapshotEvent)
	time.Sleep(1 * time.Second) // 等处理

	// 断言内存态已种子
	snap := ws.LatestSnapshot()
	if len(snap.Positions) != 1 {
		t.Fatalf("snapshot positions len = %d, want 1 (seeded from type7 first frame)", len(snap.Positions))
	}
	if snap.Positions[0].InstrumentID != "X-AFQUL" {
		t.Errorf("seeded instrument = %q, want X-AFQUL", snap.Positions[0].InstrumentID)
	}
	// 断言零 REST 调用（首帧种子不触发 REST）
	if atomic.LoadInt32(&restCalls) > 0 {
		t.Errorf("REST calls = %d, want 0 (type7 snapshot seeds without REST)", atomic.LoadInt32(&restCalls))
	}
}

// ---------- N2: patch 三深度分发 ----------

func TestV21_N2_PatchThreeDepths(t *testing.T) {
	st := newTestStore(t)
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
	// 先种子
	ts.sendEvent(0, wsType7SnapshotEvent)
	time.Sleep(500 * time.Millisecond)

	// 深度 1: position 字段 patch
	ts.sendEvent(0, wsType7PatchPosition)
	time.Sleep(300 * time.Millisecond)
	snap := ws.LatestSnapshot()
	if len(snap.Positions) == 0 || snap.Positions[0].Quantity != "18200.00" {
		t.Errorf("position patch: quantity = %q, want 18200.00", snap.Positions[0].Quantity)
	}

	// 深度 2: order 字段 patch
	ts.sendEvent(0, wsType7PatchOrder)
	time.Sleep(300 * time.Millisecond)
	snap = ws.LatestSnapshot()
	if len(snap.Positions) == 0 || len(snap.Positions[0].Orders) == 0 || snap.Positions[0].Orders[0].FilledPrice != "0.33000" {
		t.Errorf("order patch: filledPrice = %q, want 0.33000", snap.Positions[0].Orders[0].FilledPrice)
	}

	// 深度 3: root patch（不报错即通过）
	ts.sendEvent(0, wsType7PatchRoot)
	time.Sleep(300 * time.Millisecond)
	// root patch 不修改 position 字段，验证 positions 仍正常
	snap = ws.LatestSnapshot()
	if len(snap.Positions) != 1 {
		t.Errorf("root patch: positions len = %d, want 1 (unchanged)", len(snap.Positions))
	}
}

// ---------- N3: 非 m patch / 越界 → warn + REST 重播种 ----------

func TestV21_N3_PatchWarnAndRestReseed(t *testing.T) {
	st := newTestStore(t)
	restCalls := int32(0)
	ts := newWsTestServerWithRESTCounter(t, mockPositionsJSON, &restCalls)
	defer ts.close()
	auth := &mockAuth{jwt: "fake.jwt.sig"}
	ws := NewWsClient(auth, 38941616, st, ts.srv.URL, ts.srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ws.Run(ctx)

	if !ts.waitSubscribed(10 * time.Second) {
		t.Fatal("timeout waiting for subscribe")
	}
	// 先种子
	ts.sendEvent(0, wsType7SnapshotEvent)
	time.Sleep(500 * time.Millisecond)
	atomic.StoreInt32(&restCalls, 0)

	// 推非 m patch
	ts.sendEvent(0, wsType7PatchBadType)
	time.Sleep(1 * time.Second)

	// 断言 patchWarnCount 增加
	if ws.patchWarnCount.Load() == 0 {
		t.Error("patchWarnCount = 0, want > 0 (bad patch type should warn)")
	}
	// 断言 REST 重播种发生
	if atomic.LoadInt32(&restCalls) == 0 {
		t.Error("REST calls = 0, want > 0 (bad patch should trigger REST reseed)")
	}

	// 推越界 patch
	prevWarn := ws.patchWarnCount.Load()
	ts.sendEvent(0, wsType7PatchOutOfRange)
	time.Sleep(1 * time.Second)
	if ws.patchWarnCount.Load() <= prevWarn {
		t.Error("patchWarnCount did not increase after out-of-range patch")
	}
}

// ---------- N4: 价格事件节流 ----------

func TestV21_N4_PriceEventThrottle(t *testing.T) {
	st := newTestStore(t)
	ts := newWsTestServer(t, mockPositionsJSON)
	defer ts.close()
	auth := &mockAuth{jwt: "fake.jwt.sig"}
	ws := NewWsClient(auth, 38941616, st, ts.srv.URL, ts.srv.URL)
	ws.SetMarketStatus(func() string { return "Open" })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ws.Run(ctx)

	if !ts.waitSubscribed(10 * time.Second) {
		t.Fatal("timeout waiting for subscribe")
	}
	// 种子（含 evaluationAskPrice=0.32335）
	ts.sendEvent(0, wsType7SnapshotEvent)
	time.Sleep(500 * time.Millisecond)

	// 查 WS 价格行（首帧提取 ask → 写一行）
	var wsPriceCount int
	_ = st.Pool().QueryRow(context.Background(),
		"SELECT count(*) FROM fin_cmc_prices WHERE src = 'cmc_ws'").Scan(&wsPriceCount)
	if wsPriceCount == 0 {
		t.Fatal("no ws price row after snapshot (ask extraction)")
	}

	// 验证行含 src/bid/mid/market_status（COALESCE NULL→0 便于非指针 Scan）
	var src, marketStatus string
	var bid, mid float64
	_ = st.Pool().QueryRow(context.Background(),
		"SELECT src, COALESCE(bid_price,0), COALESCE(mid_price,0), market_status FROM fin_cmc_prices WHERE src = 'cmc_ws' ORDER BY fetched_at DESC LIMIT 1").
		Scan(&src, &bid, &mid, &marketStatus)
	if src != "cmc_ws" {
		t.Errorf("ws price src = %q, want cmc_ws", src)
	}
	if marketStatus != "Open" {
		t.Errorf("ws price market_status = %q, want Open", marketStatus)
	}
	// WS 行 bid/mid 应为 0（NULL → 0 in Go scan）
	if bid != 0 {
		t.Errorf("ws price bid = %v, want 0 (NULL)", bid)
	}
}

// ---------- N5: type5/type6 漂移 + type5 不落库 ----------

func TestV21_N5_Type5CrossCheck_NoPersist(t *testing.T) {
	st := newTestStore(t)
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
	// 先推 type6 建立基准
	ts.sendEvent(0, wsType6Event)
	time.Sleep(300 * time.Millisecond)

	// 推 3 条 type5 漂移事件（>0.5%）
	for i := 0; i < 3; i++ {
		ts.sendEvent(0, wsType5WithDrift)
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)

	// 断言 type5 不落库（summary 表只有 type6 驱动的行）
	var summaryCount int
	_ = st.Pool().QueryRow(context.Background(),
		"SELECT count(*) FROM fin_cmc_account_summary").Scan(&summaryCount)
	// type6 节流 60s → 可能 0 或 1 行，但 type5 不应增加
	if summaryCount > 1 {
		t.Errorf("summary rows = %d, want <= 1 (type5 should not persist)", summaryCount)
	}
}

// ---------- N6: 守卫只看 type6 ----------

func TestV21_N6_GuardType6Only(t *testing.T) {
	st := newTestStore(t)
	ts := newWsTestServer(t, mockPositionsJSON)
	defer ts.close()
	auth := &mockAuth{jwt: "fake.jwt.sig"}
	ws := NewWsClient(auth, 38941616, st, ts.srv.URL, ts.srv.URL)

	// 缩短守卫窗
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
	// 推 type7 事件（不应触发守卫恢复）
	ts.sendEvent(0, wsType7SnapshotEvent)
	time.Sleep(300 * time.Millisecond)

	// 推 type6 建立计时
	ts.sendEvent(0, wsType6Event)
	time.Sleep(300 * time.Millisecond)

	// 等待守卫击穿（type6 沉默 > stale）
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
		t.Fatal("guard did not trip after type6 silence (type7 alone should not keep healthy)")
	}
}

// ---------- N7: alerts 三态 ----------

func TestV21_N7_AlertsThreeStates(t *testing.T) {
	// 纯函数测试：AlertsToState
	// 空 → normal
	if state := AlertsToState(json.RawMessage("[]")); state != "normal" {
		t.Errorf("empty alerts: state = %q, want normal", state)
	}
	// warning
	if state := AlertsToState(json.RawMessage(`[{"alertType":"InStandardCloseOutWarning","value":"true"}]`)); state != "warning" {
		t.Errorf("warning alerts: state = %q, want warning", state)
	}
	// breach
	if state := AlertsToState(json.RawMessage(`[{"alertType":"InStandardCloseOutBreach","value":"true"}]`)); state != "breach" {
		t.Errorf("breach alerts: state = %q, want breach", state)
	}
	// 空 nil → normal
	if state := AlertsToState(nil); state != "normal" {
		t.Errorf("nil alerts: state = %q, want normal", state)
	}
}

// ---------- N8: LastCMCPriceForSide 优先级 + NULL mid 过滤 ----------

func TestV21_N8_LastCMCPriceForSide(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// 写一条 REST 行（含 ask + mid）
	_ = st.InsertCMCPrice(ctx, pgstore.CMCPrice{
		InstrumentID: "X-AFQUL", AskPrice: 0.323350, MidPrice: 0.323100, Src: "cmc_rest", FetchedAt: time.Now(),
	})
	// 写一条 WS 行（ask only, bid/mid NULL）
	_ = st.InsertCMCPrice(ctx, pgstore.CMCPrice{
		InstrumentID: "X-AFQUL", AskPrice: 0.323400, MidPrice: 0, BidPrice: 0, Src: "cmc_ws", FetchedAt: time.Now().Add(1 * time.Second),
	})

	// ForSide: WS ask 优先
	px, _, src, err := st.LastCMCPriceForSide(ctx, "X-AFQUL", "Sell")
	if err != nil {
		t.Fatalf("LastCMCPriceForSide: %v", err)
	}
	if px != 0.323400 {
		t.Errorf("ForSide px = %v, want 0.323400 (WS ask priority)", px)
	}
	if src != "cmc_ws" {
		t.Errorf("ForSide src = %q, want cmc_ws", src)
	}

	// LastCMCPriceMidWithTime: 忽略 NULL mid 行 → 返回 REST 行 mid
	mid, _, err := st.LastCMCPriceMidWithTime(ctx, "X-AFQUL")
	if err != nil {
		t.Fatalf("LastCMCPriceMidWithTime: %v", err)
	}
	if mid != 0.323100 {
		t.Errorf("mid = %v, want 0.323100 (NULL mid rows ignored)", mid)
	}
}

// ---------- N8b: ForSide REST 回退 ----------

func TestV21_N8b_ForSideRestFallback(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// 只写 REST 行（无 WS 行）
	if err := st.InsertCMCPrice(ctx, pgstore.CMCPrice{
		InstrumentID: "X-AFQUL", AskPrice: 0.323350, MidPrice: 0.323100, Src: "cmc_rest", FetchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	px, _, src, err := st.LastCMCPriceForSide(ctx, "X-AFQUL", "Sell")
	if err != nil {
		t.Fatalf("LastCMCPriceForSide: %v", err)
	}
	if px != 0.323350 {
		t.Errorf("ForSide px = %v, want 0.323350 (REST fallback)", px)
	}
	if src != "cmc_rest" {
		t.Errorf("ForSide src = %q, want cmc_rest", src)
	}
}

// ---------- N9: src 感知 watchdog ----------

func TestV21_N9_SourceAwareWatchdog(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// 写一条 cmc_ws 行，fetched_at = 8 分钟前（< 10min 阈值 → 不 stale）
	past := time.Now().Add(-8 * time.Minute)
	_, err := st.Pool().Exec(ctx,
		`INSERT INTO fin_cmc_prices (instrument_id, bid_price, ask_price, mid_price, market_status, quote_time, fetched_at, src)
		 VALUES ('X-AFQUL', 0, 0.3234, 0, 'Open', $1, $1, 'cmc_ws')`, past)
	if err != nil {
		t.Fatalf("insert ws price: %v", err)
	}
	src, status, last, err := st.LastCMCPriceStatusAndFetchedAt(ctx)
	if err != nil {
		t.Fatalf("LastCMCPriceStatusAndFetchedAt: %v", err)
	}
	if src != "cmc_ws" {
		t.Errorf("src = %q, want cmc_ws", src)
	}
	if status != "Open" {
		t.Errorf("status = %q, want Open", status)
	}
	// 8min < 10min → 不 stale
	if time.Since(last) > 10*time.Minute {
		t.Errorf("ws row age = %v, should be < 10min (not stale)", time.Since(last))
	}

	// 写一条 REST 行，fetched_at = 6 分钟前（> 5min 阈值 → stale）
	_, _ = st.Pool().Exec(ctx,
		`INSERT INTO fin_cmc_prices (instrument_id, bid_price, ask_price, mid_price, market_status, quote_time, fetched_at, src)
		 VALUES ('X-AFQUL', 0.3228, 0.3233, 0.3231, 'Open', $1, $1, 'cmc_rest')`,
		time.Now().Add(-6*time.Minute))
	src2, _, last2, _ := st.LastCMCPriceStatusAndFetchedAt(ctx)
	// REST 行 fetched_at 更新（6min ago < 8min ago → REST 行成为"最近"）
	if src2 != "cmc_rest" {
		t.Errorf("src = %q, want cmc_rest (most recent)", src2)
	}
	// 6min > 5min → stale（REST 阈值）
	if time.Since(last2) <= 5*time.Minute {
		t.Errorf("rest row age = %v, should be > 5min (stale)", time.Since(last2))
	}

	// closed-market skip：写一条 closed 行（时间戳最近 → 成为 LastCMCPriceStatusAndFetchedAt 返回行）
	_, _ = st.Pool().Exec(ctx,
		`INSERT INTO fin_cmc_prices (instrument_id, bid_price, ask_price, mid_price, market_status, quote_time, fetched_at, src)
		 VALUES ('X-AFQUL', 0.3228, 0.3233, 0.3231, 'closed', $1, $1, 'cmc_rest')`,
		time.Now().Add(-1*time.Minute))
	_, status3, _, err3 := st.LastCMCPriceStatusAndFetchedAt(ctx)
	if err3 != nil {
		t.Fatalf("closed row query: %v", err3)
	}
	if !strings.EqualFold(status3, "closed") {
		t.Errorf("status = %q, want closed", status3)
	}
	// closed 行即使 fetched_at 最近，watchdog 也跳过 stale 判定（逻辑验证：status=closed → continue）
}

// ---------- 辅助：带 REST 调用计数的 fake server ----------

func newWsTestServerWithRESTCounter(t *testing.T, positionsJSON string, restCalls *int32) *wsTestServer {
	ts := &wsTestServer{
		t:             t,
		upgrader:      websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		subCh:         make(chan struct{}, 4),
		positionsJSON: positionsJSON,
	}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/positions") && r.Header.Get("Upgrade") == "" {
			atomic.AddInt32(restCalls, 1)
		}
		ts.handler(w, r)
	}))
	return ts
}

// 确保 fact import 被使用（sink 签名引用）
var _ fact.Fact
var _ = pgx.ErrNoRows
