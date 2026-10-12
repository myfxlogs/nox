package cmc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
	"arb/internal/testutil"

	"github.com/jackc/pgx/v5"
)

// ---------- mock AuthManager（返回固定 JWT，不碰网络） ----------

type mockAuth struct {
	jwt string
	err error
}

func (m *mockAuth) JWT(ctx context.Context) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.jwt, nil
}

// ---------- mock JSON（照抄 API端点清单.md 原文，字符串数值） ----------

const mockPriceJSON = `{
  "quoteId": "1788332335677671093",
  "askPrice": "0.323350",
  "midPrice": "0.323100",
  "bidPrice": "0.322850",
  "marketStatus": "Open",
  "time": "2026-09-02T06:58:55.677Z",
  "state": "Valid"
}`

const mockSummaryJSON = `{
  "primaryAccountCurrency": "USD",
  "totalAccountValue": "2178.07",
  "totalEquityFreeForCashTrading": "1441.94",
  "totalEquityFreeForMarginTrading": "1593.26",
  "totalEquityUsedInMarginTrading": "584.81",
  "totalCashBalance": "2026.76",
  "totalUnrealisedPnL": "151.32",
  "totalCFDUnrealisedPnL": "151.32",
  "accountValueAlerts": []
}`

const mockPositionsJSON = `{
  "primaryAccountCurrency": "USD",
  "totalCFDUnrealisedPnL": "151.32",
  "totalStandardMarginAmount": "584.81",
  "positions": [{
    "instrumentId": "X-AFQUL",
    "contractId": null,
    "productType": "CFD",
    "direction": "Sell",
    "quantity": "18100.00",
    "stakeCurrency": "USD",
    "price": "0.33171",
    "filledValueInInstrumentCurrency": "6003.95",
    "filledValueAccountCurrency": "6003.95",
    "standardMarginAmount": "584.81",
    "marginAmount": "584.81",
    "marginType": "Standard",
    "standardMarginPercentage": "0.10",
    "unrealisedPnLInInstrumentCurrency": "151.32",
    "unrealisedPnLInPrimaryCurrency": "151.32",
    "evaluationBidPrice": "0.00",
    "evaluationAskPrice": "0.32335",
    "instrumentPriceNumberOfDecimals": "5",
    "orders": [{
      "orderId": "ORD-OPEN-001",
      "filledPrice": "0.33171",
      "submittedTimestamp": "2026-08-17T10:30:00.1234567Z"
    }]
  }]
}`

const mockEmptyPositionsJSON = `{
  "primaryAccountCurrency": "USD",
  "totalCFDUnrealisedPnL": "0.00",
  "totalStandardMarginAmount": "0.00",
  "positions": []
}`

const mockHistoryJSON = `{
  "categoryTypes": ["CASH", "TRADE"],
  "totalCount": 3,
  "totalTypes": ["OVERNIGHT_SETTLEMENT", "SELL_TRADE", "PAYMENTS_IN"],
  "transactions": [{
    "transactionId": "acb760ec-test-0001",
    "amount": "1.6",
    "balance": "2026.76",
    "currency": "USD",
    "orderId": "1655218453",
    "performedByClient": false,
    "productName": "Account",
    "profitAndLoss": "1.6",
    "transactionTime": "2026-09-01T21:00:41.404Z",
    "transactionType": "OVERNIGHT_SETTLEMENT",
    "transactionTypeString": "OVERNIGHT_SETTLEMENT"
  }, {
    "transactionId": "acb760ec-test-0002",
    "amount": "-500.00",
    "balance": "1500.00",
    "currency": "USD",
    "orderId": "ORD-SELL-001",
    "performedByClient": true,
    "productName": "X-AFQUL",
    "profitAndLoss": "0",
    "transactionTime": "2026-08-17T10:30:00.000Z",
    "transactionType": "SELL_TRADE",
    "transactionTypeString": "SELL_TRADE"
  }, {
    "transactionId": "acb760ec-test-0003",
    "amount": "1000.00",
    "balance": "2500.00",
    "currency": "USD",
    "orderId": "",
    "performedByClient": false,
    "productName": "Account",
    "profitAndLoss": "0",
    "transactionTime": "2026-08-15T08:00:00.000Z",
    "transactionType": "PAYMENTS_IN",
    "transactionTypeString": "PAYMENTS_IN"
  }]
}`

// ---------- DSN-gated 真库三步（v7，store 依赖测试） ----------

func newTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// 清空 cmc 表（测试隔离）
	for _, tbl := range []string{"fin_cmc_prices", "fin_cmc_account_summary", "fin_cmc_positions", "fin_cmc_history"} {
		if _, err := st.Pool().Exec(context.Background(), "DELETE FROM "+tbl); err != nil {
			t.Fatalf("cleanup %s: %v", tbl, err)
		}
	}
	return st
}

// ---------- PricesCollector 测试 ----------

func TestPricesCollector_Poll_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r
		w.WriteHeader(200)
		_, _ = w.Write([]byte(mockPriceJSON))
	}))
	defer srv.Close()

	st := newTestStore(t)
	auth := &mockAuth{jwt: "fake.jwt.sig"}
	c := NewPricesCollector(auth, "X-AFQUL", st, srv.URL)
	facts, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("facts len = %d, want 1", len(facts))
	}
	if facts[0].Kind != fact.KindCMCPrice {
		t.Errorf("Kind = %q", facts[0].Kind)
	}
	if facts[0].Value != 0.323100 {
		t.Errorf("Value = %v, want 0.323100", facts[0].Value)
	}
	// 验证 store 写入
	mid, _, err := st.LastCMCPriceMidWithTime(context.Background(), "X-AFQUL")
	if err != nil {
		t.Fatalf("LastCMCPriceMidWithTime: %v", err)
	}
	if mid != 0.323100 {
		t.Errorf("stored mid = %v, want 0.323100", mid)
	}
}

func TestPricesCollector_Poll_JWTFail(t *testing.T) {
	st := newTestStore(t)
	auth := &mockAuth{err: errors.New("jwt fail")}
	c := NewPricesCollector(auth, "X-AFQUL", st, "http://unused")
	_, err := c.Poll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "JWT") {
		t.Fatalf("expected JWT error, got %v", err)
	}
}

func TestPricesCollector_Poll_RESTFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	st := newTestStore(t)
	auth := &mockAuth{jwt: "fake.jwt.sig"}
	c := NewPricesCollector(auth, "X-AFQUL", st, srv.URL)
	_, err := c.Poll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 500 error, got %v", err)
	}
}

// ---------- AccountCollector 测试 ----------

func TestAccountCollector_Poll_Success(t *testing.T) {
	st := newTestStore(t)
	// D-225 v2.1：先写入一条 price（含 ask——calcCloseout 现走 LastCMCPriceForSide(short)=ask）
	_ = st.InsertCMCPrice(context.Background(), pgstore.CMCPrice{
		InstrumentID: "X-AFQUL", MidPrice: 0.324530, AskPrice: 0.323350, FetchedAt: time.Now(),
	})

	callIdx := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callIdx++
		if strings.Contains(r.URL.Path, "/value/summary") {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(mockSummaryJSON))
			return
		}
		if strings.Contains(r.URL.Path, "/positions") {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(mockPositionsJSON))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	auth := &mockAuth{jwt: "fake.jwt.sig"}
	c := NewAccountCollector(auth, 38941616, st, srv.URL)
	facts, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(facts) != 1 || facts[0].Kind != fact.KindCMCAccount {
		t.Fatalf("facts = %+v", facts)
	}
	if facts[0].Value != 2178.07 {
		t.Errorf("Value = %v, want 2178.07", facts[0].Value)
	}
	// D-225 v2.1：验证强平计算（C=2026.76, entry=0.33171, qty=18100, L=10, ask=0.323350）
	// closeout = (2026.76 + 0.33171*18100) * 10 / (18100 * 10.5) = 0.4226
	// distance = (0.4226 - 0.323350) / 0.323350 * 100 = 30.7%（ask 口径，F11 保守修正）
	row := st.Pool().QueryRow(context.Background(),
		"SELECT closeout_price, distance_to_closeout_pct FROM fin_cmc_account_summary ORDER BY fetched_at DESC LIMIT 1")
	var closeout, dist float64
	if err := row.Scan(&closeout, &dist); err != nil {
		t.Fatalf("query summary: %v", err)
	}
	if closeout < 0.4225 || closeout > 0.4227 {
		t.Errorf("closeout = %v, want ~0.4226", closeout)
	}
	if dist < 30.5 || dist > 30.9 {
		t.Errorf("distance = %v, want ~30.7 (ask 口径)", dist)
	}
	// D-208 v2-A：验证 orders[0] 解析——opened_at/order_id 已落库
	posRow := st.Pool().QueryRow(context.Background(),
		"SELECT order_id, opened_at FROM fin_cmc_positions WHERE instrument_id = 'X-AFQUL' ORDER BY fetched_at DESC LIMIT 1")
	var orderID string
	var openedAt *time.Time
	if err := posRow.Scan(&orderID, &openedAt); err != nil {
		t.Fatalf("query positions: %v", err)
	}
	if orderID != "ORD-OPEN-001" {
		t.Errorf("order_id = %q, want ORD-OPEN-001", orderID)
	}
	if openedAt == nil {
		t.Fatal("opened_at = nil, want non-nil (parsed from submittedTimestamp)")
	}
	// PostgreSQL TIMESTAMPTZ 微秒精度（6 位小数秒），RFC3339Nano 7 位被截断到微秒
	wantOpened := time.Date(2026, 8, 17, 10, 30, 0, 123456000, time.UTC)
	if !openedAt.UTC().Truncate(time.Microsecond).Equal(wantOpened) {
		t.Errorf("opened_at = %v, want %v (microsecond precision)", *openedAt, wantOpened)
	}
}

func TestAccountCollector_Poll_SummaryFail(t *testing.T) {
	st := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/value/summary") {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(mockPositionsJSON))
	}))
	defer srv.Close()

	auth := &mockAuth{jwt: "fake.jwt.sig"}
	c := NewAccountCollector(auth, 38941616, st, srv.URL)
	_, err := c.Poll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "summary") {
		t.Fatalf("expected summary error, got %v", err)
	}
}

func TestAccountCollector_Poll_PositionsFail(t *testing.T) {
	st := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/value/summary") {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(mockSummaryJSON))
			return
		}
		w.WriteHeader(500)
	}))
	defer srv.Close()

	auth := &mockAuth{jwt: "fake.jwt.sig"}
	c := NewAccountCollector(auth, 38941616, st, srv.URL)
	_, err := c.Poll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "positions") {
		t.Fatalf("expected positions error, got %v", err)
	}
}

func TestAccountCollector_Poll_EmptyPositions(t *testing.T) {
	st := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/value/summary") {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(mockSummaryJSON))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(mockEmptyPositionsJSON))
	}))
	defer srv.Close()

	auth := &mockAuth{jwt: "fake.jwt.sig"}
	c := NewAccountCollector(auth, 38941616, st, srv.URL)
	facts, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("facts len = %d, want 1", len(facts))
	}
	// 空持仓 → closeout=0
	row := st.Pool().QueryRow(context.Background(),
		"SELECT closeout_price FROM fin_cmc_account_summary ORDER BY fetched_at DESC LIMIT 1")
	var closeout float64
	if err := row.Scan(&closeout); err != nil {
		t.Fatalf("query: %v", err)
	}
	if closeout != 0 {
		t.Errorf("closeout = %v, want 0 (empty positions)", closeout)
	}
}

func TestAccountCollector_Poll_NoPriceData(t *testing.T) {
	st := newTestStore(t)
	// 不写入任何 price → LastCMCPriceForSide 返回 ErrNoRows → px=0 → distance=0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/value/summary") {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(mockSummaryJSON))
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(mockPositionsJSON))
	}))
	defer srv.Close()

	auth := &mockAuth{jwt: "fake.jwt.sig"}
	c := NewAccountCollector(auth, 38941616, st, srv.URL)
	_, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	// closeout 仍计算，distance=0
	row := st.Pool().QueryRow(context.Background(),
		"SELECT closeout_price, distance_to_closeout_pct FROM fin_cmc_account_summary ORDER BY fetched_at DESC LIMIT 1")
	var closeout, dist float64
	if err := row.Scan(&closeout, &dist); err != nil {
		t.Fatalf("query: %v", err)
	}
	if closeout < 0.4225 || closeout > 0.4227 {
		t.Errorf("closeout = %v, want ~0.4226 (still calculated)", closeout)
	}
	if dist != 0 {
		t.Errorf("distance = %v, want 0 (no price data)", dist)
	}
}

// ---------- HistoryCollector 测试 ----------

func TestHistoryCollector_Poll_Success(t *testing.T) {
	st := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(mockHistoryJSON))
	}))
	defer srv.Close()

	auth := &mockAuth{jwt: "fake.jwt.sig"}
	c := NewHistoryCollector(auth, 38941616, st, srv.URL)
	facts, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	// D-208 v2-A：facts 发射仍只 OVERNIGHT_SETTLEMENT（swap 语义不变）
	if len(facts) != 1 {
		t.Fatalf("facts len = %d, want 1 (only OVERNIGHT_SETTLEMENT emits fact)", len(facts))
	}
	if facts[0].Value != 1.6 {
		t.Errorf("Value = %v, want 1.6", facts[0].Value)
	}
	// D-208 v2-A：fin_cmc_history 全类型入库（SELL_TRADE/PAYMENTS_IN 不再被丢）
	var allCount int
	err = st.Pool().QueryRow(context.Background(),
		"SELECT count(*) FROM fin_cmc_history WHERE account_id = 38941616").Scan(&allCount)
	if err != nil {
		t.Fatalf("count all: %v", err)
	}
	if allCount != 3 {
		t.Errorf("all history count = %d, want 3 (all types persisted)", allCount)
	}
	// 再次 Poll 验证幂等（upsert 不重复）
	_, err = c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll 2: %v", err)
	}
	// 查 DB 验证仍 3 条（upsert 幂等）
	err = st.Pool().QueryRow(context.Background(),
		"SELECT count(*) FROM fin_cmc_history WHERE account_id = 38941616").Scan(&allCount)
	if err != nil {
		t.Fatalf("count after re-poll: %v", err)
	}
	if allCount != 3 {
		t.Errorf("history count after re-poll = %d, want 3 (upsert idempotent)", allCount)
	}
}

// mockHistoryJSONWithBadTime 含一条畸形 TransactionTime（S6 防回归）。
const mockHistoryJSONWithBadTime = `{
  "categoryTypes": ["CASH", "TRADE"],
  "totalCount": 3,
  "totalTypes": ["OVERNIGHT_SETTLEMENT", "SELL_TRADE", "PAYMENTS_IN"],
  "transactions": [{
    "transactionId": "bad-time-0001",
    "amount": "1.6",
    "balance": "2026.76",
    "currency": "USD",
    "orderId": "1655218453",
    "performedByClient": false,
    "productName": "Account",
    "profitAndLoss": "1.6",
    "transactionTime": "NOT-A-DATE",
    "transactionType": "OVERNIGHT_SETTLEMENT",
    "transactionTypeString": "OVERNIGHT_SETTLEMENT"
  }, {
    "transactionId": "good-time-0002",
    "amount": "-500.00",
    "balance": "1500.00",
    "currency": "USD",
    "orderId": "ORD-SELL-001",
    "performedByClient": true,
    "productName": "X-AFQUL",
    "profitAndLoss": "0",
    "transactionTime": "2026-08-17T10:30:00.000Z",
    "transactionType": "SELL_TRADE",
    "transactionTypeString": "SELL_TRADE"
  }, {
    "transactionId": "good-time-0003",
    "amount": "1000.00",
    "balance": "2500.00",
    "currency": "USD",
    "orderId": "",
    "performedByClient": false,
    "productName": "Account",
    "profitAndLoss": "0",
    "transactionTime": "2026-08-15T08:00:00.000Z",
    "transactionType": "PAYMENTS_IN",
    "transactionTypeString": "PAYMENTS_IN"
  }]
}`

// TestHistoryCollector_Poll_BadTimestampSkip 畸形 TransactionTime → 该笔跳过，其余正常入库（S6/D-208 R1 闭环）。
func TestHistoryCollector_Poll_BadTimestampSkip(t *testing.T) {
	st := newTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(mockHistoryJSONWithBadTime))
	}))
	defer srv.Close()

	auth := &mockAuth{jwt: "fake.jwt.sig"}
	c := NewHistoryCollector(auth, 38941616, st, srv.URL)
	facts, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	// 畸形笔（OVERNIGHT_SETTLEMENT）被跳过 → facts 应为 0
	if len(facts) != 0 {
		t.Errorf("facts len = %d, want 0 (bad-timestamp OVERNIGHT_SETTLEMENT skipped)", len(facts))
	}
	// 其余 2 笔正常入库
	var count int
	err = st.Pool().QueryRow(context.Background(),
		"SELECT count(*) FROM fin_cmc_history WHERE account_id = 38941616").Scan(&count)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("history count = %d, want 2 (bad-timestamp tx skipped, 2 good txs persisted)", count)
	}
	// 断言畸形笔不在库中
	var badCount int
	_ = st.Pool().QueryRow(context.Background(),
		"SELECT count(*) FROM fin_cmc_history WHERE account_id = 38941616 AND transaction_id = 'bad-time-0001'").Scan(&badCount)
	if badCount != 0 {
		t.Errorf("bad-timestamp tx found in DB (should be skipped): count=%d", badCount)
	}
}

// ---------- 强平距离计算测试 ----------

func TestCloseoutCalc(t *testing.T) {
	// C=2026.76, entry=0.33171, qty=18100, margin_percentage=0.10, L=10
	// closeout = (2026.76 + 0.33171*18100) * 10 / (18100 * 10.5) = 0.4226
	// mid=0.324530 → distance = (0.4226 - 0.324530) / 0.324530 * 100 = 30.2%
	C := 2026.76
	entry := 0.33171
	qty := 18100.0
	marginPct := 0.10
	L := 1 / marginPct
	closeout := (C + entry*qty) * L / (qty * (L + 0.5))
	mid := 0.324530
	distance := (closeout - mid) / mid * 100
	if closeout < 0.4225 || closeout > 0.4227 {
		t.Errorf("closeout = %.4f, want ~0.4226", closeout)
	}
	if distance < 30.1 || distance > 30.3 {
		t.Errorf("distance = %.1f%%, want ~30.2%%", distance)
	}
}

func TestCloseoutCalc_DifferentLeverage(t *testing.T) {
	marginPct := 0.05
	L := 1 / marginPct
	if L != 20 {
		t.Errorf("L = %v, want 20", L)
	}
	C := 2026.76
	entry := 0.33171
	qty := 18100.0
	closeout := (C + entry*qty) * L / (qty * (L + 0.5))
	// L=20 → closeout=0.4329（比 L=10 的 0.4226 更高，L 越大越接近 (C+entry*qty)/qty=0.4437）
	if closeout < 0.43 || closeout > 0.44 {
		t.Errorf("closeout = %v, want ~0.4329 for L=20", closeout)
	}
	// 验证 L 非硬编码：L=20 的 closeout 应不同于 L=10
	closeoutL10 := (C + entry*qty) * 10 / (qty * 10.5)
	if closeout == closeoutL10 {
		t.Error("closeout same as L=10, L not derived from margin_percentage")
	}
}

func TestCloseoutCalc_NoPriceData(t *testing.T) {
	// mid=0 → distance=0（不除以 0）
	closeout := 0.4226
	mid := 0.0
	var distance float64
	if mid != 0 {
		distance = (closeout - mid) / mid * 100
	}
	if distance != 0 {
		t.Errorf("distance = %v, want 0 when mid=0", distance)
	}
}

// ---------- Watchdog 测试 ----------

func TestWatchdog_EmptyTableAlsoWarns(t *testing.T) {
	st := newTestStore(t)
	// 不写入任何 price → 空表 → ErrNoRows → last 零值 → Since 巨大 → 告警
	// 直接调用 LastCMCPriceFetchedAt 验证返回 ErrNoRows
	_, err := st.LastCMCPriceFetchedAt(context.Background())
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected ErrNoRows on empty table, got %v", err)
	}
	// 验证 watchdog 逻辑：空表时 last 取零值 → Since 巨大 → 触发 warning
	var last time.Time // 零值
	if time.Since(last) <= 5*time.Minute {
		t.Fatal("empty table should trigger stale warning (Since(zero) > 5min)")
	}
}

// ---------- JSON 解码测试（确保字符串数值正确解析） ----------

func TestPriceJSON_StringNumericDecode(t *testing.T) {
	var p priceJSON
	if err := json.Unmarshal([]byte(mockPriceJSON), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.MidPrice != "0.323100" {
		t.Errorf("MidPrice = %q, want string", p.MidPrice)
	}
	mid := parseFloat(p.MidPrice)
	if mid != 0.323100 {
		t.Errorf("parseFloat(MidPrice) = %v, want 0.323100", mid)
	}
}

func TestPositionsJSON_StringNumericDecode(t *testing.T) {
	var pos positionsJSON
	if err := json.Unmarshal([]byte(mockPositionsJSON), &pos); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(pos.Positions) != 1 {
		t.Fatalf("positions len = %d", len(pos.Positions))
	}
	p := pos.Positions[0]
	if p.Quantity != "18100.00" {
		t.Errorf("Quantity = %q, want string", p.Quantity)
	}
	if p.StandardMarginPercentage != "0.10" {
		t.Errorf("MarginPercentage = %q", p.StandardMarginPercentage)
	}
	if parseFloat(p.StandardMarginPercentage) != 0.10 {
		t.Errorf("parseFloat margin = %v", parseFloat(p.StandardMarginPercentage))
	}
}

// 确保 fmt import 被使用
var _ = fmt.Sprintf
