package exchange

// D-334ab 位点①测试：binance income（FUNDING_FEE 实数轨）采集器。
// T1 unit（httptest mock + fake sink：字段换算/签名头/游标参数）；
// T2 DSN（真 pgstore sink 两轮同数据 → ON CONFLICT 幂等仍 2 行）。

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// fakeIncomeSink T1 用内存 sink：记录 InsertIncomeEvents 收到的行，匹配行为可注入。
type fakeIncomeSink struct {
	maxTS    time.Time
	hasMax   bool
	match    func(venue, symbol, env string) (int64, int16, int, error)
	inserted []pgstore.IncomeEvent
}

func (f *fakeIncomeSink) MaxIncomeEventTS(_ context.Context, _, _ string) (time.Time, bool, error) {
	return f.maxTS, f.hasMax, nil
}
func (f *fakeIncomeSink) MatchOpenLegForIncome(_ context.Context, venue, symbol, env string) (int64, int16, int, error) {
	if f.match != nil {
		return f.match(venue, symbol, env)
	}
	return 0, 0, 0, nil
}
func (f *fakeIncomeSink) InsertIncomeEvents(_ context.Context, evs []pgstore.IncomeEvent) (int64, error) {
	f.inserted = append(f.inserted, evs...)
	return int64(len(evs)), nil
}

// incomeMockHandler 固定回 2 行 income（无视 startTime——T2 靠它逼出 ON CONFLICT 幂等）。
func incomeMockHandler(t *testing.T, apiKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-MBX-APIKEY"); got != apiKey {
			t.Errorf("X-MBX-APIKEY = %q, want %q", got, apiKey)
		}
		q := r.URL.Query()
		if q.Get("incomeType") != "FUNDING_FEE" {
			t.Errorf("incomeType = %q, want FUNDING_FEE", q.Get("incomeType"))
		}
		if q.Get("limit") != "1000" {
			t.Errorf("limit = %q, want 1000", q.Get("limit"))
		}
		if q.Get("signature") == "" || q.Get("timestamp") == "" {
			t.Errorf("signed request missing signature/timestamp: %v", q)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"symbol": "XAUUSDT", "incomeType": "FUNDING_FEE", "income": "-1.52670000", "asset": "USDT", "time": float64(1788163200000), "tranId": float64(253001)},
			{"symbol": "XAUUSDT", "incomeType": "FUNDING_FEE", "income": "0.5", "asset": "USDT", "time": float64(1788192000000), "tranId": float64(253002)},
		})
	}
}

// TestD334ab_BinanceIncome_RunOnce T1：httptest mock 2 行 → 一轮落 2 事件、
// 字段换算对（time ms→ts、income 字符串→float、tranId）、游标首拉=now-24h。
func TestD334ab_BinanceIncome_RunOnce(t *testing.T) {
	srv := httptest.NewServer(incomeMockHandler(t, "test-key"))
	defer srv.Close()

	sink := &fakeIncomeSink{}
	col := NewBinanceIncomeCollector(Config{Client: srv.Client()}, sink, func() []BinanceIncomeAccount {
		return []BinanceIncomeAccount{{ID: 25, APIKey: "test-key", APISecret: "test-secret", BaseURL: srv.URL}}
	})
	n, err := col.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if n != 2 {
		t.Fatalf("written=%d, want 2", n)
	}
	if len(sink.inserted) != 2 {
		t.Fatalf("inserted=%d, want 2", len(sink.inserted))
	}
	first := sink.inserted[0]
	if first.Venue != VenueBinance || first.Symbol != "XAUUSDT" || first.IncomeType != "FUNDING_FEE" {
		t.Errorf("identity fields wrong: %+v", first)
	}
	if math.Abs(first.AmountUSD-(-1.5267)) > 1e-8 {
		t.Errorf("amount want -1.5267, got %v", first.AmountUSD)
	}
	if !first.Ts.Equal(time.UnixMilli(1788163200000)) {
		t.Errorf("ts want 1788163200000ms, got %v", first.Ts)
	}
	if first.TranID != 253001 {
		t.Errorf("tranId want 253001, got %v", first.TranID)
	}
	// 无 OPEN 腿命中（fake match 0 hits）→ position_id/leg_no 留 NULL（不猜）
	if first.PositionID != nil || first.LegNo != nil {
		t.Errorf("unmatched row must leave position/leg NULL, got %+v", first)
	}
	if first.VenueType != "perp" {
		t.Errorf("venue_type want perp, got %v", first.VenueType)
	}
}

// TestD334ab_BinanceIncome_CursorFromSink T1b：sink 有游标 → startTime=MAX(ts)+1ms。
func TestD334ab_BinanceIncome_CursorFromSink(t *testing.T) {
	var gotStart string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotStart = r.URL.Query().Get("startTime")
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer srv.Close()

	cursor := time.UnixMilli(1788163200000)
	sink := &fakeIncomeSink{maxTS: cursor, hasMax: true}
	col := NewBinanceIncomeCollector(Config{Client: srv.Client()}, sink, func() []BinanceIncomeAccount {
		return []BinanceIncomeAccount{{ID: 25, APIKey: "k", APISecret: "s", BaseURL: srv.URL}}
	})
	if _, err := col.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if want := fmt.Sprint(cursor.Add(time.Millisecond).UnixMilli()); gotStart != want {
		t.Errorf("startTime want cursor+1ms=%s, got %s", want, gotStart)
	}
}

// TestD334ab_BinanceIncome_IdempotentDSN T2：真 pgstore sink，两轮同数据 → 表内仍 2 行
// 且第二轮 inserted=0（ON CONFLICT (venue,account_id,tran_id) DO NOTHING）。
// 红=摘 DO NOTHING → 第二轮唯一约束报错/重写。
func TestD334ab_BinanceIncome_IdempotentDSN(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE income_events`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	srv := httptest.NewServer(incomeMockHandler(t, "test-key"))
	defer srv.Close()
	col := NewBinanceIncomeCollector(Config{Client: srv.Client()}, st, func() []BinanceIncomeAccount {
		return []BinanceIncomeAccount{{ID: 25, APIKey: "test-key", APISecret: "test-secret", BaseURL: srv.URL}}
	})

	n1, err := col.RunOnce(ctx)
	if err != nil || n1 != 2 {
		t.Fatalf("round1 written=%d err=%v, want 2/nil", n1, err)
	}
	n2, err := col.RunOnce(ctx)
	if err != nil {
		t.Fatalf("round2 must stay error-free (DO NOTHING), got %v", err)
	}
	if n2 != 0 {
		t.Fatalf("round2 written=%d, want 0（幂等）", n2)
	}
	var count int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM income_events`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("table rows=%d, want 2（红=摘唯一约束/DO NOTHING 后重写）", count)
	}
	// sink 级直插双写（不经 RunOnce 的逐账户 fail-open 错误吞噬）：幂等=第二插 err=nil 且 0 行；
	// 红=摘 DO NOTHING → 唯一约束报错；摘唯一约束 → 第二插写成功 n=1。
	ev := []pgstore.IncomeEvent{{Venue: "binance", VenueType: "perp", Symbol: "XAUUSDT", IncomeType: "FUNDING_FEE", AmountUSD: 1.0, TranID: 999001, Ts: time.Now()}}
	if n, err := st.InsertIncomeEvents(ctx, ev); err != nil || n != 1 {
		t.Fatalf("sink first insert: n=%d err=%v, want 1/nil", n, err)
	}
	if n, err := st.InsertIncomeEvents(ctx, ev); err != nil || n != 0 {
		t.Fatalf("sink re-insert: n=%d err=%v, want 0/nil（红=摘幂等守卫后报错或重写）", n, err)
	}
}
