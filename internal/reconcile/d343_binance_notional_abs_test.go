// d343_binance_notional_abs_test.go — D-343：binance positionRisk 空头腿 notional
// 负值 → 全仓 initMargin=notional/leverage 得负 → build 侧 >0 守卫拒 → margin=0
// 「无法获得保证金数据」（业主报 binance:perp XAUUSDT 实证；D-322 funding 符号同族坑）。
// T（先红后绿）：空头 cross 行 notional="-4375.50" lev="20" → InitialMargin=+218.775
// （红=现公式出 −218.775 被下游守卫拒）；多头 cross 行回归锚不受影响。
package reconcile

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestD343_BinanceShortCrossNotionalAbs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		positions := []map[string]any{
			// 空头全仓 XAUUSDT：positionAmt<0，notional 负值（positionRisk 实盘实证形态）
			{"symbol": "XAUUSDT", "positionAmt": "-0.5", "entryPrice": "8751", "unRealizedProfit": "-1.0", "positionSide": "BOTH", "marginType": "cross", "isolatedWallet": "0", "leverage": "20", "notional": "-4375.50"},
			// 多头全仓 ETH：notional 正值（回归锚——Abs 不误伤）
			{"symbol": "ETHUSDT", "positionAmt": "0.4", "entryPrice": "2488.14", "unRealizedProfit": "0.56", "positionSide": "BOTH", "marginType": "cross", "isolatedWallet": "0", "leverage": "2", "notional": "995.256"},
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
		t.Fatalf("want 2 orders, got %d", len(orders))
	}
	// 空头全仓：initMargin = |notional|/lev = 4375.50/20 = 218.775（红=现 −218.775）
	if orders[0].Side != "SELL" || orders[0].PosSide != "short" {
		t.Errorf("order[0] 应为空头, got side=%s posSide=%s", orders[0].Side, orders[0].PosSide)
	}
	if math.Abs(orders[0].InitialMargin-218.775) > 0.01 {
		t.Errorf("order[0] InitialMargin = %.4f, want +218.775（红=现公式负值 −218.775）", orders[0].InitialMargin)
	}
	// 多头全仓回归锚：995.256/2 = 497.628
	if math.Abs(orders[1].InitialMargin-497.628) > 0.01 {
		t.Errorf("order[1] InitialMargin = %.4f, want 497.628（多头回归锚）", orders[1].InitialMargin)
	}
}
