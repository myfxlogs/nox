// d565_leverage_test.go —— D-565 S2 T2 锚：venue 杠杆实读三所矩阵 + 读败 fail-closed。
// gate=accounts(position_mode)+get_leverage/{contract}（dual→双 margin 取 min）；
// binance=/v1/positionSide/dual 探测 + /v2/account positions[] 腿侧匹配；
// okx=/api/v5/account/leverage-info posSide 腿侧行。全链零硬编——读败→error 不猜。
package exec

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestD565_GateLeverageRead dual 模式按腿侧双 margin 取 min（isolated=7/cross=5→5）。
func TestD565_GateLeverageRead(t *testing.T) {
	var dualSides []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/futures/usdt/accounts":
			_, _ = w.Write([]byte(`{"position_mode":"dual"}`))
		case "/futures/usdt/get_leverage/BTC_USDT":
			dualSides = append(dualSides, r.URL.Query().Get("dual_side"))
			switch r.URL.Query().Get("pos_margin_mode") {
			case "isolated":
				_, _ = w.Write([]byte(`{"lever":"7"}`))
			case "cross":
				_, _ = w.Write([]byte(`{"lever":"5"}`))
			default:
				t.Errorf("dual 缺 pos_margin_mode query")
			}
		case "/futures/usdt/contracts/BTC_USDT": // warnLeverageMax 附属读
			_, _ = w.Write([]byte(`{"leverage_max":"50"}`))
		default:
			t.Errorf("unexpected gate path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, false)
	// 卖腿→dual_short
	lev, err := e.ReadVenueLeverage(context.Background(), "BTC_USDT", true)
	if err != nil || lev != 5 {
		t.Fatalf("dual sell leg: lev=%v err=%v, want 5（双 margin min）", lev, err)
	}
	for _, ds := range dualSides {
		if ds != "dual_short" {
			t.Fatalf("sell leg dual_side=%q want dual_short", ds)
		}
	}
	// 读败→error（get_leverage 404）
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/futures/usdt/accounts" {
			_, _ = w.Write([]byte(`{"position_mode":"single"}`))
			return
		}
		w.WriteHeader(500)
	}))
	defer srv2.Close()
	e2 := NewCEXExecutor("gate", "perp", "k", "s", "", srv2.URL, false)
	if _, err := e2.ReadVenueLeverage(context.Background(), "BTC_USDT", false); err == nil {
		t.Fatal("get_leverage 读败应 error（fail-closed 不猜）")
	}
}

// TestGateLeverage_SingleDualSide venue 语义锚（10-17 复审席亲测三通道）：
// dual_side 与 position_mode 无关恒必填——single 模式裸发=400
// INVALID_PARAM_VALUE（枚举只收 dual_long|dual_short，dual_both 永不发）；
// 修后 query 断言 dual_side=dual_long、路径 200 读中。
func TestGateLeverage_SingleDualSide(t *testing.T) {
	var dualSides []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/futures/usdt/accounts":
			_, _ = w.Write([]byte(`{"position_mode":"single","in_dual_mode":false}`))
		case "/futures/usdt/get_leverage/TRX_USDT":
			ds := r.URL.Query().Get("dual_side")
			dualSides = append(dualSides, ds)
			if ds == "dual_both" {
				t.Error("dual_both 禁发（枚举白名单只收 dual_long|dual_short）")
			}
			if ds != "dual_long" && ds != "dual_short" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"label":"INVALID_PARAM_VALUE"}`))
				return
			}
			_, _ = w.Write([]byte(`{"lever":"10"}`))
		case "/futures/usdt/contracts/TRX_USDT": // warnLeverageMax 附属读
			_, _ = w.Write([]byte(`{"leverage_max":"50"}`))
		default:
			t.Errorf("unexpected gate path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, false)
	lev, err := e.ReadVenueLeverage(context.Background(), "TRX_USDT", false)
	if err != nil || lev != 10 {
		t.Fatalf("single 模式: lev=%v err=%v, want 10", lev, err)
	}
	if len(dualSides) == 0 || dualSides[0] != "dual_long" {
		t.Fatalf("single 模式 dual_side=%v want [dual_long]（恒必填）", dualSides)
	}
}

// TestD565_BinanceLeverageRead hedge 模式腿侧匹配 + 无匹配行 error。
func TestD565_BinanceLeverageRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/positionSide/dual":
			_, _ = w.Write([]byte(`{"dualSidePosition":true}`))
		case "/v2/account":
			_, _ = w.Write([]byte(`{"positions":[
				{"symbol":"BTCUSDT","positionSide":"LONG","leverage":"10","isolated":false},
				{"symbol":"BTCUSDT","positionSide":"SHORT","leverage":"20","isolated":true},
				{"symbol":"ETHUSDT","positionSide":"SHORT","leverage":"3","isolated":false}
			]}`))
		default:
			t.Errorf("unexpected binance path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL, false)
	// 卖腿→SHORT 行=20
	if lev, err := e.ReadVenueLeverage(context.Background(), "BTCUSDT", true); err != nil || lev != 20 {
		t.Fatalf("sell leg: lev=%v err=%v, want 20（SHORT 行）", lev, err)
	}
	// 买腿→LONG 行=10
	if lev, err := e.ReadVenueLeverage(context.Background(), "BTCUSDT", false); err != nil || lev != 10 {
		t.Fatalf("buy leg: lev=%v err=%v, want 10（LONG 行）", lev, err)
	}
	// 无匹配 symbol → error 不猜
	if _, err := e.ReadVenueLeverage(context.Background(), "SOLUSDT", true); err == nil {
		t.Fatal("无匹配行应 error（不猜）")
	}
	// one-way 模式 → BOTH 行
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/positionSide/dual":
			_, _ = w.Write([]byte(`{"dualSidePosition":false}`))
		case "/v2/account":
			_, _ = w.Write([]byte(`{"positions":[{"symbol":"BTCUSDT","positionSide":"BOTH","leverage":"8","isolated":false}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv2.Close()
	e2 := NewCEXExecutor("binance", "perp", "k", "s", "", srv2.URL, false)
	if lev, err := e2.ReadVenueLeverage(context.Background(), "BTCUSDT", true); err != nil || lev != 8 {
		t.Fatalf("one-way: lev=%v err=%v, want 8（BOTH 行）", lev, err)
	}
}

// TestD565_OKXLeverageInfo posSide 腿侧行 + net 兜底 + code 非零 error。
func TestD565_OKXLeverageInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/leverage-info" {
			t.Errorf("unexpected okx path %s", r.URL.Path)
		}
		if r.URL.Query().Get("instId") != "BTC-USDT-SWAP" || r.URL.Query().Get("mgnMode") != "cross" {
			t.Errorf("query=%s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":"0","data":[{"posSide":"long","lever":"10"},{"posSide":"short","lever":"3"}]}`))
	}))
	defer srv.Close()
	e := NewCEXExecutor("okx", "perp", "k", "s", "p", srv.URL, false)
	if lev, err := e.ReadVenueLeverage(context.Background(), "BTC-USDT-SWAP", true); err != nil || lev != 3 {
		t.Fatalf("sell leg: lev=%v err=%v, want 3（short 行）", lev, err)
	}
	// net 单向兜底
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":"0","data":[{"posSide":"net","lever":"6"}]}`))
	}))
	defer srv2.Close()
	e2 := NewCEXExecutor("okx", "perp", "k", "s", "p", srv2.URL, false)
	if lev, err := e2.ReadVenueLeverage(context.Background(), "BTC-USDT-SWAP", false); err != nil || lev != 6 {
		t.Fatalf("net: lev=%v err=%v, want 6", lev, err)
	}
	// code 非零 → error
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":"50011","data":[]}`))
	}))
	defer srv3.Close()
	e3 := NewCEXExecutor("okx", "perp", "k", "s", "p", srv3.URL, false)
	if _, err := e3.ReadVenueLeverage(context.Background(), "BTC-USDT-SWAP", false); err == nil {
		t.Fatal("code 非零应 error（不猜）")
	}
}
