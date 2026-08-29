// cex_positions_test.go — CEXPositionQuerier 单测（D-121）。
// 覆盖：OKX/Gate/Binance perp 持仓解析 + 空仓跳过 + spot 返回空 + venueKey。
package reconcile

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestD121_OKXPositions OKX perp 持仓解析（long/short 分列，pos=合约数）。
func TestD121_OKXPositions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/positions" {
			t.Errorf("want /api/v5/account/positions, got %s", r.URL.Path)
		}
		resp := map[string]any{
			"code": "0",
			"msg":  "",
			"data": []map[string]any{
				{"instId": "ETH-USDT-SWAP", "posSide": "long", "pos": "40", "avgPx": "2488.5", "upl": "1.23", "fee": "-0.05", "cTime": "1693526400000", "margin": "497.70"},
				{"instId": "BTC-USDT-SWAP", "posSide": "short", "pos": "10", "avgPx": "60000", "upl": "-2.5", "fee": "-0.03", "cTime": "1693526500000", "margin": "600.00"},
				{"instId": "ETH-USDT-SWAP", "posSide": "net", "pos": "0", "avgPx": "0", "upl": "0", "fee": "0", "cTime": "0", "margin": "0"},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

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
	// D-146 S5 微件③：OKX margin 字段映射到 InitialMargin
	if orders[0].InitialMargin != 497.70 {
		t.Errorf("order[0] InitialMargin = %.4f, want 497.70 (OKX margin)", orders[0].InitialMargin)
	}
	// BTC short
	if orders[1].Symbol != "BTC-USDT-SWAP" || orders[1].Side != "SELL" || orders[1].Volume != 10 {
		t.Errorf("order[1] mismatch: %+v", orders[1])
	}
	if orders[1].InitialMargin != 600.00 {
		t.Errorf("order[1] InitialMargin = %.4f, want 600.00 (OKX margin)", orders[1].InitialMargin)
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
		// Gate 返回数组
		positions := []map[string]any{
			{"contract": "ETH_USDT", "size": 40, "entry_price": "2537.12", "unrealized_pnl": "1.5", "funding": "-0.0023"},
			{"contract": "BTC_USDT", "size": -10, "entry_price": "60000", "unrealized_pnl": "-3.0", "funding": "0.001"},
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
func TestD121_BinancePositions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v2/positionRisk" {
			t.Errorf("want /fapi/v2/positionRisk, got %s", r.URL.Path)
		}
		positions := []map[string]any{
			{"symbol": "ETHUSDT", "positionAmt": "0.4", "entryPrice": "2488.14", "unRealizedProfit": "0.56", "positionSide": "BOTH", "initialMargin": "497.63", "maintMargin": "39.81"},
			{"symbol": "BTCUSDT", "positionAmt": "-0.1", "entryPrice": "60000", "unRealizedProfit": "-1.2", "positionSide": "BOTH", "initialMargin": "6000.00", "maintMargin": "480.00"},
			{"symbol": "SOLUSDT", "positionAmt": "0", "entryPrice": "0", "unRealizedProfit": "0", "positionSide": "BOTH", "initialMargin": "0", "maintMargin": "0"},
		}
		_ = json.NewEncoder(w).Encode(positions)
	}))
	defer srv.Close()

	q := NewCEXPositionQuerier("binance", "perp", "key", "secret", "", srv.URL, false)
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
	// D-146 S5 微件③：Binance initialMargin/maintMargin 字段映射
	if orders[0].InitialMargin != 497.63 {
		t.Errorf("order[0] InitialMargin = %.4f, want 497.63 (Binance initialMargin)", orders[0].InitialMargin)
	}
	if orders[0].MaintMargin != 39.81 {
		t.Errorf("order[0] MaintMargin = %.4f, want 39.81 (Binance maintMargin)", orders[0].MaintMargin)
	}
	if orders[1].Symbol != "BTCUSDT" || orders[1].Side != "SELL" || orders[1].Volume != 0.1 {
		t.Errorf("order[1] mismatch: %+v", orders[1])
	}
	if orders[1].InitialMargin != 6000.00 || orders[1].MaintMargin != 480.00 {
		t.Errorf("order[1] margin mismatch: init=%.4f maint=%.4f, want 6000.00/480.00", orders[1].InitialMargin, orders[1].MaintMargin)
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
