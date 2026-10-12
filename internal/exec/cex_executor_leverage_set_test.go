// cex_executor_leverage_set_test.go —— D-079 锚：EnsureVenueLeverage 三 venue 设后复读矩阵。
// (a) 读5→POST→复读10=nil+POST 次数与载荷；(b) 已=10→零 POST；(c) 设后复读仍 5→复读 error；
// (d) simulated→零请求。gate 走 set_leverage 指定模式面（iso+cross 两 POST 对齐 min 读法）。
package exec

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestD079_OKXEnsurLeverage okx：读5→set-leverage POST→复读10；已达标零 POST；复读不符→err。
func TestD079_OKXEnsurLeverage(t *testing.T) {
	ctx := context.Background()
	var posts atomic.Int32
	var lever atomic.Value // string
	lever.Store("5")
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/account/set-leverage":
			posts.Add(1)
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			lever.Store("10") // 设定生效→复读=10
			_, _ = w.Write([]byte(`{"code":"0","data":[]}`))
		case "/api/v5/account/leverage-info":
			_, _ = w.Write([]byte(`{"code":"0","data":[{"posSide":"net","lever":"` + lever.Load().(string) + `"}]}`))
		default:
			t.Errorf("unexpected okx path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	e := NewCEXExecutor("okx", "perp", "k", "s", "p", srv.URL, false)
	if err := e.EnsureVenueLeverage(ctx, "XAU-USDT", false, 10); err != nil {
		t.Fatalf("set 5→10 应放行: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("应恰 1 次 POST, got %d", posts.Load())
	}
	if !strings.Contains(gotBody, `"instId":"XAU-USDT-SWAP"`) || !strings.Contains(gotBody, `"lever":"10"`) || !strings.Contains(gotBody, `"mgnMode":"cross"`) {
		t.Fatalf("body 形态错: %s", gotBody)
	}
	// 已达标 → 零新 POST
	if err := e.EnsureVenueLeverage(ctx, "XAU-USDT", false, 10); err != nil || posts.Load() != 1 {
		t.Fatalf("已=目标应零 POST: err=%v posts=%d", err, posts.Load())
	}
	// 设后复读仍 5 → error 含「复读」
	lever.Store("5")
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/account/set-leverage" {
			_, _ = w.Write([]byte(`{"code":"0","data":[]}`))
			return // 设定不生效（复读仍 5）
		}
		_, _ = w.Write([]byte(`{"code":"0","data":[{"posSide":"net","lever":"5"}]}`))
	}))
	defer stub.Close()
	e2 := NewCEXExecutor("okx", "perp", "k", "s", "p", stub.URL, false)
	if err := e2.EnsureVenueLeverage(ctx, "XAU-USDT", false, 10); err == nil || !strings.Contains(err.Error(), "复读") {
		t.Fatalf("复读仍 5 应 err 含「复读」, got %v", err)
	}
}

// TestD079_BinanceEnsureLeverage binance：读5（BOTH 行）→POST /v1/leverage→复读10。
func TestD079_BinanceEnsureLeverage(t *testing.T) {
	ctx := context.Background()
	var posts atomic.Int32
	var lever atomic.Value
	lever.Store("5")
	var gotQ string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/positionSide/dual":
			_, _ = w.Write([]byte(`{"dualSidePosition":false}`))
		case "/v1/leverage":
			posts.Add(1)
			gotQ = r.URL.RawQuery
			lever.Store("10")
			_, _ = w.Write([]byte(`{"leverage":10,"symbol":"XAUUSDT"}`))
		case "/v2/account":
			lv := lever.Load().(string)
			_, _ = w.Write([]byte(`{"positions":[{"symbol":"XAUUSDT","positionSide":"BOTH","leverage":"` + lv + `","isolated":false}]}`))
		default:
			t.Errorf("unexpected binance path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL, false)
	if err := e.EnsureVenueLeverage(ctx, "XAUUSDT", true, 10); err != nil {
		t.Fatalf("set 5→10 应放行: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("应恰 1 次 POST, got %d", posts.Load())
	}
	if !strings.Contains(gotQ, "symbol=XAUUSDT") || !strings.Contains(gotQ, "leverage=10") || !strings.Contains(gotQ, "signature=") {
		t.Fatalf("query 形态错: %s", gotQ)
	}
}

// TestD079_GateEnsureLeverage gate dual：读 min(iso=7,cross=5)=5→set_leverage×2
// （isolated 带腿侧 dual_side + cross 合约级）→复读 min=10。single 模式不带 dual_side。
func TestD079_GateEnsureLeverage(t *testing.T) {
	ctx := context.Background()
	var posts atomic.Int32
	var isoQ, crossQ string
	var iso, cross atomic.Value
	iso.Store("7")
	cross.Store("5")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/futures/usdt/accounts":
			_, _ = w.Write([]byte(`{"position_mode":"dual"}`))
		case strings.HasPrefix(r.URL.Path, "/futures/usdt/get_leverage/"):
			switch r.URL.Query().Get("pos_margin_mode") {
			case "isolated":
				_, _ = w.Write([]byte(`{"lever":"` + iso.Load().(string) + `"}`))
			case "cross":
				_, _ = w.Write([]byte(`{"lever":"` + cross.Load().(string) + `"}`))
			}
		case strings.HasPrefix(r.URL.Path, "/futures/usdt/positions/BTC_USDT/set_leverage"):
			posts.Add(1)
			switch r.URL.Query().Get("margin_mode") {
			case "isolated":
				isoQ = r.URL.RawQuery
				iso.Store("10")
			case "cross":
				crossQ = r.URL.RawQuery
				cross.Store("10")
			}
			_, _ = w.Write([]byte(`{"contract":"BTC_USDT"}`))
		case strings.HasPrefix(r.URL.Path, "/futures/usdt/contracts/"):
			_, _ = w.Write([]byte(`{"leverage_max":"50"}`)) // warnLeverageMax 附属读
		default:
			t.Errorf("unexpected gate %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, false)
	if err := e.EnsureVenueLeverage(ctx, "BTC_USDT", true, 10); err != nil {
		t.Fatalf("dual set 应放行: %v", err)
	}
	if posts.Load() != 2 {
		t.Fatalf("iso+cross 两域各一次 POST, got %d", posts.Load())
	}
	if !strings.Contains(isoQ, "margin_mode=isolated") || !strings.Contains(isoQ, "dual_side=dual_short") || !strings.Contains(isoQ, "leverage=10") {
		t.Fatalf("isolated 载荷错: %s", isoQ)
	}
	if !strings.Contains(crossQ, "margin_mode=cross") || strings.Contains(crossQ, "dual_side") || !strings.Contains(crossQ, "leverage=10") {
		t.Fatalf("cross 载荷错（官方示例 cross 不带 dual_side）: %s", crossQ)
	}
}

// TestD079_EnsureSimulated simulated → 直接 nil 且零请求（纸面不触 venue）。
func TestD079_EnsureSimulated(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	e := NewCEXExecutor("okx", "perp", "k", "s", "p", srv.URL, true)
	if err := e.EnsureVenueLeverage(context.Background(), "XAU-USDT", false, 10); err != nil {
		t.Fatalf("simulated 应 nil: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("simulated 应零请求, got %d", hits.Load())
	}
	// target≤0 防御兜底：非 simulated 也零请求
	e2 := NewCEXExecutor("okx", "perp", "k", "s", "p", srv.URL, false)
	if err := e2.EnsureVenueLeverage(context.Background(), "XAU-USDT", false, 0); err != nil || hits.Load() != 0 {
		t.Fatalf("target=0 应 nil+零请求: %v hits=%d", err, hits.Load())
	}
}
