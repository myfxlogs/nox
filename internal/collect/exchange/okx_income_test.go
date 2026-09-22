package exchange

// D-385 S2/S3 测试：okx bills→income_events 采集器。
// T1 unit（httptest mock + fakeIncomeSink 复用 D-334ab）：字段换算/签名头/subType 过滤/
// 非数值 billId 丢弃/sim 环境 env=demo/bills-archive 空洞分支。

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// okxBillsHandler 回 3 行 bill：173 支出 + 174 收入 + 非 funding subType（须被过滤）。
func okxBillsHandler(t *testing.T, apiKey, passphrase string, wantSim bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("OK-ACCESS-KEY"); got != apiKey {
			t.Errorf("OK-ACCESS-KEY = %q, want %q", got, apiKey)
		}
		if r.Header.Get("OK-ACCESS-SIGN") == "" || r.Header.Get("OK-ACCESS-TIMESTAMP") == "" {
			t.Error("signed request missing sign/timestamp")
		}
		if got := r.Header.Get("OK-ACCESS-PASSPHRASE"); got != passphrase {
			t.Errorf("OK-ACCESS-PASSPHRASE = %q, want %q", got, passphrase)
		}
		if wantSim && r.Header.Get("x-simulated-trading") != "1" {
			t.Error("simulated account must carry x-simulated-trading:1")
		}
		if r.URL.Path != "/api/v5/account/bills" && r.URL.Path != "/api/v5/account/bills-archive" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if q := r.URL.Query(); q.Get("limit") != "100" || q.Get("instType") != "" {
			t.Errorf("query = %v, want limit=100 且无 instType（R0-A1：该维度不成立不下发）", q)
		}
		if r.URL.Query().Get("after") != "" {
			// 续拉页（本测试只有一页数据，after 之后回空——终止分页）
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "msg": "", "data": []map[string]any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "0", "msg": "",
			"data": []map[string]any{
				{"billId": "900002", "instId": "XAU-USDT-SWAP", "subType": "174", "balChg": "0.5", "ccy": "USDT", "ts": "1788192000000"},
				{"billId": "900001", "instId": "XAU-USDT-SWAP", "subType": "173", "balChg": "-1.5267", "ccy": "USDT", "ts": "1788163200000"},
				{"billId": "900004", "instId": "XAU-USDT-SWAP", "subType": "174", "balChg": "0.001", "ccy": "BTC", "ts": "1788170000000"}, // 非 USDT，R0-A2 须丢弃
				{"billId": "900000", "instId": "XAU-USDT-SWAP", "subType": "8", "balChg": "-0.01", "ccy": "USDT", "ts": "1788160000000"},  // 非 funding，须过滤
				{"billId": "abc", "instId": "XAU-USDT-SWAP", "subType": "174", "balChg": "9", "ccy": "USDT", "ts": "1788160000000"},       // 非数值 billId，须丢弃
			},
		})
	}
}

// TestD385_OKXIncome_RunOnce T1：一轮落 2 事件（173/174），subType 过滤、
// 非数值 billId 丢弃、字段换算对（balChg 符号直写、ts ms→time、billId→tran_id）。
func TestD385_OKXIncome_RunOnce(t *testing.T) {
	srv := httptest.NewServer(okxBillsHandler(t, "test-key", "test-pass", false))
	defer srv.Close()

	sink := &fakeIncomeSink{}
	col := NewOKXIncomeCollector(Config{Client: srv.Client()}, sink, func() []OKXIncomeAccount {
		return []OKXIncomeAccount{{ID: 7, APIKey: "test-key", APISecret: "s", Passphrase: "test-pass", BaseURL: srv.URL}}
	})
	n, err := col.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if n != 2 {
		t.Fatalf("written=%d, want 2（173+174；subType=8 与非数值 billId 须过滤）", n)
	}
	first := sink.inserted[0]
	if first.Venue != VenueOKX || first.Symbol != "XAU-USDT-SWAP" || first.IncomeType != "FUNDING_FEE" {
		t.Errorf("identity fields wrong: %+v", first)
	}
	if math.Abs(first.AmountUSD-0.5) > 1e-8 {
		t.Errorf("amount want 0.5, got %v", first.AmountUSD)
	}
	if first.TranID != 900002 {
		t.Errorf("tranId want 900002, got %v", first.TranID)
	}
	if !first.Ts.Equal(time.UnixMilli(1788192000000)) {
		t.Errorf("ts want 1788192000000ms, got %v", first.Ts)
	}
	if first.PositionID != nil || first.LegNo != nil {
		t.Errorf("unmatched row must leave position/leg NULL, got %+v", first)
	}
}

// TestD385_OKXIncome_SimEnv T1b（锚 C）：simulated 账户的 bill 行 env=demo 匹配，
// 唯一命中挂 demo 仓腿；请求带 x-simulated-trading:1。
func TestD385_OKXIncome_SimEnv(t *testing.T) {
	srv := httptest.NewServer(okxBillsHandler(t, "k", "p", true))
	defer srv.Close()

	var gotVenue, gotEnv string
	sink := &fakeIncomeSink{match: func(venue, symbol, env string) (int64, int16, int, error) {
		gotVenue, gotEnv = venue, env
		return 498, 1, 1, nil
	}}
	col := NewOKXIncomeCollector(Config{Client: srv.Client()}, sink, func() []OKXIncomeAccount {
		return []OKXIncomeAccount{{ID: 8, APIKey: "k", APISecret: "s", Passphrase: "p", BaseURL: srv.URL, Simulated: true}}
	})
	if _, err := col.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if gotVenue != "okx:perp" || gotEnv != "demo" {
		t.Errorf("match args venue=%q env=%q, want okx:perp/demo", gotVenue, gotEnv)
	}
	ev := sink.inserted[0]
	if ev.PositionID == nil || *ev.PositionID != 498 || ev.LegNo == nil || *ev.LegNo != 1 {
		t.Errorf("want position_id=498 leg_no=1, got %+v", ev)
	}
}

// TestD385_OKXIncome_ArchiveFallback T1c：cursor<now-7d → 先打 bills-archive 再打 bills。
func TestD385_OKXIncome_ArchiveFallback(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "msg": "", "data": []map[string]any{}})
	}))
	defer srv.Close()

	sink := &fakeIncomeSink{maxTS: time.Now().Add(-8 * 24 * time.Hour), hasMax: true}
	col := NewOKXIncomeCollector(Config{Client: srv.Client()}, sink, func() []OKXIncomeAccount {
		return []OKXIncomeAccount{{ID: 9, APIKey: "k", APISecret: "s", Passphrase: "p", BaseURL: srv.URL}}
	})
	if _, err := col.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(paths) != 2 || paths[0] != "/api/v5/account/bills-archive" || paths[1] != "/api/v5/account/bills" {
		t.Errorf("request paths=%v, want [bills-archive bills]（cursor 超窗须先补档）", paths)
	}
}

// TestD385_OKXIncome_NoArchiveWithinWindow T1d：cursor 在 7 天内 → 只打 bills（不打 archive）。
func TestD385_OKXIncome_NoArchiveWithinWindow(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "msg": "", "data": []map[string]any{}})
	}))
	defer srv.Close()

	sink := &fakeIncomeSink{maxTS: time.Now().Add(-24 * time.Hour), hasMax: true}
	col := NewOKXIncomeCollector(Config{Client: srv.Client()}, sink, func() []OKXIncomeAccount {
		return []OKXIncomeAccount{{ID: 9, APIKey: "k", APISecret: "s", Passphrase: "p", BaseURL: srv.URL}}
	})
	if _, err := col.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/api/v5/account/bills" {
		t.Errorf("request paths=%v, want [bills]（窗口内不碰 archive）", paths)
	}
}
