// cex_positions_test.go — CEXPositionQuerier 单测（D-121）。
// 覆盖：OKX/Gate/Binance perp 持仓解析 + 空仓跳过 + spot 返回空 + venueKey。
package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestD121_OKXPositions OKX perp 持仓解析（long/short 分列，pos=合约数）。
// D-146 S5 微件③ 返修：保证金优先 imr（初始保证金），margin 回落；mmr 映射 MaintMargin。
// D-146 微件③ 追加：sim 环境 imr 实证为恒定假值（$1 名义也占 ~$333），demo 不展示（宁缺毋假）。
func TestD121_OKXPositions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/positions" {
			t.Errorf("want /api/v5/account/positions, got %s", r.URL.Path)
		}
		resp := map[string]any{
			"code": "0",
			"msg":  "",
			"data": []map[string]any{
				{"instId": "ETH-USDT-SWAP", "posSide": "long", "pos": "40", "avgPx": "2488.5", "upl": "1.23", "fee": "-0.05", "cTime": "1693526400000", "margin": "497.70", "imr": "497.63", "mmr": "39.81"},
				{"instId": "BTC-USDT-SWAP", "posSide": "short", "pos": "10", "avgPx": "60000", "upl": "-2.5", "fee": "-0.03", "cTime": "1693526500000", "margin": "600.00", "imr": "600.00", "mmr": "48.00"},
				{"instId": "ETH-USDT-SWAP", "posSide": "net", "pos": "0", "avgPx": "0", "upl": "0", "fee": "0", "cTime": "0", "margin": "0", "imr": "0", "mmr": "0"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// simulated=true（demo）：imr 假值→initMargin=0（宁缺毋假）
	q := NewCEXPositionQuerier("okx", "perp", "key", "secret", "pass", srv.URL, true)
	orders, err := q.QueryOpenedOrders(context.Background())
	if err != nil {
		t.Fatalf("okx positions: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("want 2 orders (pos=0 skipped), got %d", len(orders))
	}
	// ETH long
	if orders[0].Symbol != "ETH-USDT-SWAP" || orders[0].Side != "BUY" || orders[0].Volume != 40 {
		t.Errorf("order[0] mismatch: %+v", orders[0])
	}
	if orders[0].OpenPrice != 2488.5 || orders[0].Profit != 1.23 || orders[0].Commission != -0.05 {
		t.Errorf("order[0] fields mismatch: %+v", orders[0])
	}
	if orders[0].Ticket != "ETH-USDT-SWAP" {
		t.Errorf("order[0] Ticket should = Symbol for CEX aggregate: %s", orders[0].Ticket)
	}
	// D-146 微件③ 追加：sim 环境 imr 假值→initMargin=0（宁缺毋假）
	if orders[0].InitialMargin != 0 {
		t.Errorf("order[0] InitialMargin = %.4f, want 0 (sim imr 假值宁缺毋假)", orders[0].InitialMargin)
	}
	// mmr 仍透传（维持保证金非假值）
	if orders[0].MaintMargin != 39.81 {
		t.Errorf("order[0] MaintMargin = %.4f, want 39.81 (OKX mmr)", orders[0].MaintMargin)
	}
	// BTC short
	if orders[1].Symbol != "BTC-USDT-SWAP" || orders[1].Side != "SELL" || orders[1].Volume != 10 {
		t.Errorf("order[1] mismatch: %+v", orders[1])
	}
	if orders[1].InitialMargin != 0 || orders[1].MaintMargin != 48.00 {
		t.Errorf("order[1] margin mismatch: init=%.4f maint=%.4f, want 0/48.00 (sim init=0, mmr)", orders[1].InitialMargin, orders[1].MaintMargin)
	}
}

// TestD146S5_OKXMarginRealTrue 真金环境（simulated=false）imr 正常透传（D-146 微件③ 追加回归锚）。
func TestD146S5_OKXMarginRealTrue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"code": "0",
			"data": []map[string]any{
				{"instId": "ETH-USDT-SWAP", "posSide": "long", "pos": "40", "avgPx": "2488.5", "upl": "1.23", "fee": "-0.05", "cTime": "1693526400000", "margin": "497.70", "imr": "497.63", "mmr": "39.81"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// simulated=false（真金）：imr 真值正常透传
	q := NewCEXPositionQuerier("okx", "perp", "key", "secret", "pass", srv.URL, false)
	orders, err := q.QueryOpenedOrders(context.Background())
	if err != nil {
		t.Fatalf("okx positions: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("want 1 order, got %d", len(orders))
	}
	if orders[0].InitialMargin != 497.63 {
		t.Errorf("真金 imr 应透传，InitialMargin = %.4f, want 497.63", orders[0].InitialMargin)
	}
}

// TestD146S5_OKXMarginFallback OKX imr 缺失时 margin 回落（D-146 S5 微件③ 返修）。
// sim 环境 imr 假值置 0 优先于 margin 回落（宁缺毋假）。
func TestD146S5_OKXMarginFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"code": "0",
			"data": []map[string]any{
				// imr 缺失（空字符串）→ 回落 margin
				{"instId": "ETH-USDT-SWAP", "posSide": "long", "pos": "40", "avgPx": "2488.5", "upl": "1.23", "fee": "-0.05", "cTime": "1693526400000", "margin": "497.70", "imr": "", "mmr": ""},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// simulated=true（demo）：imr 缺失→margin 回落=497.70，但 sim 环境置 0（宁缺毋假）
	q := NewCEXPositionQuerier("okx", "perp", "key", "secret", "pass", srv.URL, true)
	orders, err := q.QueryOpenedOrders(context.Background())
	if err != nil {
		t.Fatalf("okx positions: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("want 1 order, got %d", len(orders))
	}
	// sim 环境：margin 回落后仍被 sim 置 0
	if orders[0].InitialMargin != 0 {
		t.Errorf("sim 环境 margin 回落后仍置 0，InitialMargin = %.4f, want 0", orders[0].InitialMargin)
	}
	// mmr 缺失 → MaintMargin=0
	if orders[0].MaintMargin != 0 {
		t.Errorf("mmr 缺失 MaintMargin = %.4f, want 0", orders[0].MaintMargin)
	}
}

// TestD121_GatePositions Gate perp 持仓解析（size 正负表示方向，Volume=合约数×quanto 转基础币）。
func TestD121_GatePositions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 合约规格查询
		if strings.HasPrefix(r.URL.Path, "/futures/usdt/contracts/") {
			contract := strings.TrimPrefix(r.URL.Path, "/futures/usdt/contracts/")
			quanto := "0.01" // ETH_USDT
			if contract == "BTC_USDT" {
				quanto = "0.0001"
			}
			_, _ = io.WriteString(w, `{"quanto_multiplier":"`+quanto+`"}`)
			return
		}
		if r.URL.Path != "/futures/usdt/positions" {
			t.Errorf("want /futures/usdt/positions, got %s", r.URL.Path)
		}
		// Gate 返回数组（实证字段名：unrealised_pnl/pnl_fund，D-311 R8）
		positions := []map[string]any{
			{"contract": "ETH_USDT", "size": 40, "entry_price": "2537.12", "unrealised_pnl": "1.5", "pnl_fund": "-0.0023"},
			{"contract": "BTC_USDT", "size": -10, "entry_price": "60000", "unrealised_pnl": "-3.0", "pnl_fund": "0.001"},
		}
		_ = json.NewEncoder(w).Encode(positions)
	}))
	defer srv.Close()

	q := NewCEXPositionQuerier("gate", "perp", "key", "secret", "", srv.URL, false)
	orders, err := q.QueryOpenedOrders(context.Background())
	if err != nil {
		t.Fatalf("gate positions: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("want 2 orders, got %d", len(orders))
	}
	// ETH long (size=40 张 × quanto=0.01 = 0.4 ETH)
	if orders[0].Symbol != "ETH_USDT" || orders[0].Side != "BUY" || orders[0].Volume != 0.4 {
		t.Errorf("order[0] mismatch: %+v", orders[0])
	}
	if orders[0].OpenPrice != 2537.12 || orders[0].Profit != 1.5 || orders[0].Swap != -0.0023 {
		t.Errorf("order[0] fields mismatch: %+v", orders[0])
	}
	// BTC short (size=-10 张 × quanto=0.0001 = 0.001 BTC, vol = |-10|*0.0001)
	if orders[1].Symbol != "BTC_USDT" || orders[1].Side != "SELL" || orders[1].Volume != 0.001 {
		t.Errorf("order[1] mismatch: %+v", orders[1])
	}
}

// TestD121_BinancePositions Binance perp 持仓解析（positionAmt 正负）。
// D-146 S5 微件③ 返修：fixture 用真实 positionRisk 字段（无 initialMargin/maintMargin——
// 那是账户级 /fapi/v2/account 字段，positionRisk 不返回）。per-position 保证金口径：
//   - 全仓（marginType=cross）→ notional / leverage
//   - 逐仓（marginType=isolated）→ isolatedWallet
func TestD121_BinancePositions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v2/positionRisk" {
			t.Errorf("want /fapi/v2/positionRisk, got %s", r.URL.Path)
		}
		// 真实 positionRisk 字段样本（无 initialMargin/maintMargin）
		positions := []map[string]any{
			// 全仓 ETH：notional=2488.14×0.4=995.256, leverage=2 → margin=497.628
			{"symbol": "ETHUSDT", "positionAmt": "0.4", "entryPrice": "2488.14", "unRealizedProfit": "0.56", "positionSide": "BOTH", "marginType": "cross", "isolatedWallet": "0", "leverage": "2", "notional": "995.256"},
			// 逐仓 BTC：isolatedWallet=6000.00（逐仓钱包=保证金，权威）
			{"symbol": "BTCUSDT", "positionAmt": "-0.1", "entryPrice": "60000", "unRealizedProfit": "-1.2", "positionSide": "BOTH", "marginType": "isolated", "isolatedWallet": "6000.00", "leverage": "10", "notional": "6000.00"},
			// 0 仓位跳过
			{"symbol": "SOLUSDT", "positionAmt": "0", "entryPrice": "0", "unRealizedProfit": "0", "positionSide": "BOTH", "marginType": "cross", "isolatedWallet": "0", "leverage": "5", "notional": "0"},
		}
		_ = json.NewEncoder(w).Encode(positions)
	}))
	defer srv.Close()

	q := NewCEXPositionQuerier("binance", "perp", "key", "secret", "", srv.URL+"/fapi", false)
	orders, err := q.QueryOpenedOrders(context.Background())
	if err != nil {
		t.Fatalf("binance positions: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("want 2 orders (0 pos skipped), got %d", len(orders))
	}
	if orders[0].Symbol != "ETHUSDT" || orders[0].Side != "BUY" || orders[0].Volume != 0.4 {
		t.Errorf("order[0] mismatch: %+v", orders[0])
	}
	if orders[0].OpenPrice != 2488.14 || orders[0].Profit != 0.56 {
		t.Errorf("order[0] fields mismatch: %+v", orders[0])
	}
	// D-146 S5 微件③ 返修：全仓 ETH margin = notional/leverage = 995.256/2 = 497.628
	if orders[0].InitialMargin < 497.6 || orders[0].InitialMargin > 497.7 {
		t.Errorf("order[0] InitialMargin = %.4f, want ~497.628 (cross: notional 995.256 / leverage 2)", orders[0].InitialMargin)
	}
	// MaintMargin=0：positionRisk 无此字段，走 D-124 账户分摊
	if orders[0].MaintMargin != 0 {
		t.Errorf("order[0] MaintMargin = %.4f, want 0 (positionRisk 无 maintMargin 字段)", orders[0].MaintMargin)
	}
	if orders[1].Symbol != "BTCUSDT" || orders[1].Side != "SELL" || orders[1].Volume != 0.1 {
		t.Errorf("order[1] mismatch: %+v", orders[1])
	}
	// D-146 S5 微件③ 返修：逐仓 BTC margin = isolatedWallet = 6000.00
	if orders[1].InitialMargin != 6000.00 {
		t.Errorf("order[1] InitialMargin = %.4f, want 6000.00 (isolated: isolatedWallet)", orders[1].InitialMargin)
	}
}

// TestD121_SpotReturnsNil spot venue → 返回空（无持仓概念）。
func TestD121_SpotReturnsNil(t *testing.T) {
	q := NewCEXPositionQuerier("okx", "spot", "key", "secret", "pass", "http://localhost", true)
	orders, err := q.QueryOpenedOrders(context.Background())
	if err != nil {
		t.Fatalf("spot should return nil err, got %v", err)
	}
	if orders != nil {
		t.Fatalf("spot should return nil orders, got %d", len(orders))
	}
}

// TestD121_VenueKey venueKey 格式 = venue:venueType。
func TestD121_VenueKey(t *testing.T) {
	q := NewCEXPositionQuerier("okx", "perp", "k", "s", "p", "http://x", true)
	if q.VenueKey() != "okx:perp" {
		t.Errorf("want okx:perp, got %s", q.VenueKey())
	}
}

// TestD311R8_GatePositionFields D-311 R8：Gate /futures/usdt/positions 响应实证字段名
// 四列全验——unrealised_pnl→Profit、pnl_fund→Swap、initial_margin→InitialMargin
// （cross 模式 margin 恒 0，initial_margin 优先）、maintenance_margin→MaintMargin。
// （R7③ margin 直读经 R8 实证修正为 initial_margin 优先 margin 回落。）
func TestD311R8_GatePositionFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/futures/usdt/contracts/") {
			_, _ = io.WriteString(w, `{"quanto_multiplier":"0.01"}`)
			return
		}
		positions := []map[string]any{
			{"contract": "CL_USDT", "size": 1012, "entry_price": "63.5", "unrealised_pnl": "1.0", "pnl_fund": "0.5", "initial_margin": "64.26", "margin": "0", "maintenance_margin": "32.13"},
		}
		_ = json.NewEncoder(w).Encode(positions)
	}))
	defer srv.Close()

	q := NewCEXPositionQuerier("gate", "perp", "key", "secret", "", srv.URL, false)
	orders, err := q.QueryOpenedOrders(context.Background())
	if err != nil {
		t.Fatalf("gate positions: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("want 1 order, got %d", len(orders))
	}
	if orders[0].Profit != 1.0 {
		t.Errorf("Profit=%v, want 1.0（unrealised_pnl）", orders[0].Profit)
	}
	if orders[0].Swap != 0.5 {
		t.Errorf("Swap=%v, want 0.5（pnl_fund 仓累计资金费）", orders[0].Swap)
	}
	if orders[0].InitialMargin != 64.26 {
		t.Errorf("InitialMargin=%v, want 64.26（initial_margin 优先，margin=0 cross 场景）", orders[0].InitialMargin)
	}
	if orders[0].MaintMargin != 32.13 {
		t.Errorf("MaintMargin=%v, want 32.13（maintenance_margin）", orders[0].MaintMargin)
	}
	// 既有字段回归：size=1012 张 × quanto 0.01 = 10.12 基础币
	if math.Abs(orders[0].Volume-10.12) > 1e-9 {
		t.Errorf("Volume=%v, want 10.12", orders[0].Volume)
	}
}

// TestD311R8_GateMarginFallback D-311 R8③：initial_margin 缺失（空字符串）→ margin 回落。
func TestD311R8_GateMarginFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/futures/usdt/contracts/") {
			_, _ = io.WriteString(w, `{"quanto_multiplier":"0.01"}`)
			return
		}
		positions := []map[string]any{
			{"contract": "CL_USDT", "size": 1012, "entry_price": "63.5", "unrealised_pnl": "1.0", "pnl_fund": "0", "initial_margin": "", "margin": "64.26", "maintenance_margin": ""},
		}
		_ = json.NewEncoder(w).Encode(positions)
	}))
	defer srv.Close()

	q := NewCEXPositionQuerier("gate", "perp", "key", "secret", "", srv.URL, false)
	orders, err := q.QueryOpenedOrders(context.Background())
	if err != nil {
		t.Fatalf("gate positions: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("want 1 order, got %d", len(orders))
	}
	if orders[0].InitialMargin != 64.26 {
		t.Errorf("InitialMargin=%v, want 64.26（initial_margin 缺失→margin 回落）", orders[0].InitialMargin)
	}
	if orders[0].MaintMargin != 0 {
		t.Errorf("MaintMargin=%v, want 0（maintenance_margin 缺失）", orders[0].MaintMargin)
	}
}
