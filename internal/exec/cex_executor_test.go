package exec

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCEXExecutor_BinanceOrder 验证 Binance 下单：endpoint/签名/X-MBX-APIKEY 头。
func TestCEXExecutor_BinanceOrder(t *testing.T) {
	const (
		key    = "test-key-907"
		secret = "test-secret-xyz"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// endpoint
		if r.URL.Path != "/api/v3/order" {
			t.Errorf("want path /api/v3/order, got %s", r.URL.Path)
		}
		// X-MBX-APIKEY 头
		if got := r.Header.Get("X-MBX-APIKEY"); got != key {
			t.Errorf("X-MBX-APIKEY = %q, want %q", got, key)
		}
		// 签名字段存在且正确（Binance 签名 = HMAC-SHA256(secret, 其余参数 encode)）
		q := r.URL.Query()
		sig := q.Get("signature")
		if sig == "" {
			t.Fatal("missing signature")
		}
		q.Del("signature")
		want := binanceSign(secret, q.Encode())
		if sig != want {
			t.Errorf("signature = %q, want %q", sig, want)
		}
		// 下单参数
		for k, wantV := range map[string]string{
			"symbol":    "BTCUSDT",
			"side":      "BUY",
			"type":      "MARKET",
			"quantity":  "0.0001",
			"recvWindow": "5000",
		} {
			if got := q.Get(k); got != wantV {
				t.Errorf("param %s = %q, want %q", k, got, wantV)
			}
		}
		if q.Get("timestamp") == "" {
			t.Error("missing timestamp")
		}
		_, _ = io.WriteString(w, `{"symbol":"BTCUSDT","orderId":912384,"executedQty":"0.00010000","avgPrice":"61500.5","status":"FILLED"}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("binance", "spot", key, secret, "", srv.URL, false)
	fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTCUSDT", Side: "BUY", Qty: 0.0001})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if fill.OrderID != "912384" {
		t.Errorf("OrderID = %q, want 912384", fill.OrderID)
	}
	if fill.Qty != 0.0001 {
		t.Errorf("Qty = %v, want 0.0001", fill.Qty)
	}
}

// TestCEXExecutor_BinancePerpEndpoint 验证 perp 走 /fapi/v1/order。
func TestCEXExecutor_BinancePerpEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/order" {
			t.Errorf("want /fapi/v1/order, got %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"orderId":77,"executedQty":"1.0","avgPrice":"0.3342"}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL, false)
	if _, err := e.PlaceOrder(context.Background(), Order{Symbol: "TRXUSDT", Side: "SELL", Qty: 1}); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
}

// TestCEXExecutor_BinancePerpPollsAsyncFill：demo-fapi 市价单 POST 先于撮合返回
// （status=NEW/executedQty=0，D-059 实施修正② 复核实测恒如此）→ 执行器轮询订单状态
// 拿真实成交，防生产记 0 qty 持仓（OKX 同款教训，Binance perp 复现）。
func TestCEXExecutor_BinancePerpPollsAsyncFill(t *testing.T) {
	polled := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/fapi/v1/order" && r.Method == http.MethodGet:
			polled++
			_, _ = io.WriteString(w, `{"symbol":"BTCUSDT","orderId":99,"status":"FILLED","executedQty":"0.0010","avgPrice":"71862.8"}`)
		default:
			_, _ = io.WriteString(w, `{"symbol":"BTCUSDT","orderId":99,"status":"NEW","executedQty":"0.0000","avgPrice":"0"}`)
		}
	}))
	defer srv.Close()

	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL, false)
	fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTCUSDT", Side: "BUY", Qty: 0.001})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if fill.Qty != 0.001 {
		t.Errorf("Qty = %v, want 0.001（轮询应取真实成交）", fill.Qty)
	}
	if polled == 0 {
		t.Error("应轮询订单状态，未轮询")
	}
}

// TestCEXExecutor_BinanceOrderTerminalCanceled：轮询发现订单被取消/拒绝 → 如实报错。
func TestCEXExecutor_BinanceOrderTerminalCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/fapi/v1/order" && r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"orderId":100,"status":"EXPIRED","executedQty":"0.0000","avgPrice":"0"}`)
		default:
			_, _ = io.WriteString(w, `{"orderId":100,"status":"NEW","executedQty":"0.0000"}`)
		}
	}))
	defer srv.Close()

	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL, false)
	if _, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTCUSDT", Side: "SELL", Qty: 0.001}); err == nil {
		t.Error("订单被取消应报错，got nil")
	} else if !strings.Contains(err.Error(), "EXPIRED") {
		t.Errorf("错误应含 EXPIRED 状态, got %v", err)
	}
}

// TestCEXExecutor_OKXOrder 验证 OKX 下单：body/四头/签名/模拟盘头。
func TestCEXExecutor_OKXOrder(t *testing.T) {
	const (
		key      = "okx-key-4095"
		secret   = "okx-secret-abc"
		pass     = "okx-pass-phrase"
	)
	cases := []struct {
		name      string
		simulated bool
		wantSimH  string // x-simulated-trading 期望值（"" = 不应存在）
	}{
		{"demo", true, "1"},
		{"live", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v5/trade/order" {
					t.Errorf("want /api/v5/trade/order, got %s", r.URL.Path)
				}
				if r.Method != http.MethodPost {
					t.Errorf("want POST, got %s", r.Method)
				}
				// 四头
				for _, h := range []string{"OK-ACCESS-KEY", "OK-ACCESS-SIGN", "OK-ACCESS-TIMESTAMP", "OK-ACCESS-PASSPHRASE"} {
					if r.Header.Get(h) == "" {
						t.Errorf("missing header %s", h)
					}
				}
				if r.Header.Get("OK-ACCESS-KEY") != key {
					t.Errorf("OK-ACCESS-KEY = %q", r.Header.Get("OK-ACCESS-KEY"))
				}
				if r.Header.Get("OK-ACCESS-PASSPHRASE") != pass {
					t.Errorf("OK-ACCESS-PASSPHRASE = %q", r.Header.Get("OK-ACCESS-PASSPHRASE"))
				}
				// 模拟盘头（仅 simulated）
				gotSim := r.Header.Get("x-simulated-trading")
				if tc.wantSimH == "" {
					if gotSim != "" {
						t.Errorf("live request should not have x-simulated-trading, got %q", gotSim)
					}
				} else if gotSim != tc.wantSimH {
					t.Errorf("x-simulated-trading = %q, want %q", gotSim, tc.wantSimH)
				}
				// body 字段
				var body struct {
					InstID  string `json:"instId"`
					TdMode  string `json:"tdMode"`
					Side    string `json:"side"`
					OrdType string `json:"ordType"`
					Sz      string `json:"sz"`
				}
				raw, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatalf("body json: %v (%s)", err, string(raw))
				}
				if body.InstID != "ETH-USDT" {
					t.Errorf("instId = %q, want ETH-USDT", body.InstID)
				}
				if body.TdMode != "cash" {
					t.Errorf("tdMode = %q, want cash (spot)", body.TdMode)
				}
				if body.Side != "buy" {
					t.Errorf("side = %q, want buy", body.Side)
				}
				if body.OrdType != "market" {
					t.Errorf("ordType = %q, want market", body.OrdType)
				}
				if body.Sz != "0.1" {
					t.Errorf("sz = %q, want 0.1", body.Sz)
				}
				// 签名 prehash 正确：base64(HMAC-SHA256(secret, ts+POST+path+body))
				ts := r.Header.Get("OK-ACCESS-TIMESTAMP")
				wantSign := okxSign(secret, ts+"POST"+r.URL.Path+string(raw))
				if got := r.Header.Get("OK-ACCESS-SIGN"); got != wantSign {
					t.Errorf("OK-ACCESS-SIGN mismatch")
				}
				_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"312269865356374016","sCode":"0","sMsg":"","avgPx":"3610.5","accFillSz":"0.1"}]}`)
			}))
			defer srv.Close()

			e := NewCEXExecutor("okx", "spot", key, secret, pass, srv.URL, tc.simulated)
			fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "ETH-USDT", Side: "BUY", Qty: 0.1})
			if err != nil {
				t.Fatalf("PlaceOrder: %v", err)
			}
			if fill.OrderID != "312269865356374016" {
				t.Errorf("OrderID = %q", fill.OrderID)
			}
			if fill.Qty != 0.1 || fill.Price != 3610.5 {
				t.Errorf("fill = %+v, want qty 0.1 price 3610.5", fill)
			}
		})
	}
}

// TestCEXExecutor_OKXPerpTdMode 验证 OKX perp：tdMode=cross + instId 补 -SWAP。
func TestCEXExecutor_OKXPerpTdMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		if body["tdMode"] != "cross" {
			t.Errorf("perp tdMode = %q, want cross", body["tdMode"])
		}
		if body["instId"] != "TRX-USDT-SWAP" {
			t.Errorf("perp instId = %q, want TRX-USDT-SWAP", body["instId"])
		}
		// accFillSz/avgPx 带上：避免触发成交轮询（否则 mock 收到意外的 GET）。
		_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"1","sCode":"0","accFillSz":"10","avgPx":"0.3342"}]}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("okx", "perp", "k", "s", "p", srv.URL, true)
	if _, err := e.PlaceOrder(context.Background(), Order{Symbol: "TRX-USDT-SWAP", Side: "SELL", Qty: 10}); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
}

// TestCEXExecutor_OKXPerpPosSideOverride 验证 perp 平仓显式 posSide 覆盖派生：
// SELL 平 long 须带 posSide=long（减仓），而非派生 short 新开空仓（D-059 实施修正② 实测）。
func TestCEXExecutor_OKXPerpPosSideOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]string
		_ = json.Unmarshal(raw, &body)
		if body["side"] != "sell" || body["posSide"] != "long" {
			t.Errorf("平 long: side=%q posSide=%q, want sell/long", body["side"], body["posSide"])
		}
		_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"88","sCode":"0","accFillSz":"0.01","avgPx":"71904.8"}]}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("okx", "perp", "k", "s", "p", srv.URL, true)
	// 平 long：SELL + PositionSide=long（原持仓侧），不能派生为 short。
	if _, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC-USDT-SWAP", Side: "SELL", Qty: 0.01, PositionSide: "long"}); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
}

// TestCEXExecutor_OKXOrderPollTerminalCanceled 验证轮询发现订单被取消（未成交）时报错，
// 而非静默返回 qty=0 假成交（D-059 实测：spot SELL 被模拟盘取消，旧逻辑致测试假绿）。
func TestCEXExecutor_OKXOrderPollTerminalCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"999","sCode":"0"}]}`) // 无成交 → 轮询
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"999","accFillSz":"","avgPx":"","state":"canceled"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	e := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, true)
	if _, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC-USDT", Side: "SELL", Qty: 0.001}); err == nil {
		t.Fatal("want error on canceled order, got nil")
	} else if !strings.Contains(err.Error(), "canceled") {
		t.Errorf("error should mention canceled, got %v", err)
	}
}

// TestCEXExecutor_OKXOrderError 验证 OKX 业务拒绝（code != 0）映射错误。
func TestCEXExecutor_OKXOrderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":"51000","msg":"Parameter 'sz' error.","data":[]}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, true)
	if _, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC-USDT", Side: "BUY", Qty: 99999}); err == nil {
		t.Fatal("want error on code!=0, got nil")
	} else if !strings.Contains(err.Error(), "51000") {
		t.Errorf("error should carry code 51000, got %v", err)
	}
}

// TestCEXExecutor_OKXOrderPollsFill 验证 POST 响应无成交数据时轮询订单拿真实成交
// （OKX 市价单 POST 可能先于撮合返回，accFillSz=0/avgPx 空，D-059 实施修正② 实测）。
func TestCEXExecutor_OKXOrderPollsFill(t *testing.T) {
	var gotPOST, gotGET bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/order":
			gotPOST = true
			_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"777","sCode":"0"}]}`) // 无成交 → 触发轮询
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/trade/order" && r.URL.Query().Get("ordId") == "777":
			gotGET = true
			_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"777","accFillSz":"0.001","avgPx":"70811.6","state":"filled"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	e := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, true)
	fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC-USDT", Side: "BUY", Qty: 0.001})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if !gotPOST {
		t.Error("want POST order placement")
	}
	if !gotGET {
		t.Error("want GET order-status poll")
	}
	if fill.OrderID != "777" || fill.Qty != 0.001 || fill.Price != 70811.6 {
		t.Errorf("fill = %+v, want OrderID 777 Qty 0.001 Price 70811.6", fill)
	}
}

// TestCEXExecutor_UnsupportedVenue 验证未知 venue 拒绝。
func TestCEXExecutor_UnsupportedVenue(t *testing.T) {
	e := NewCEXExecutor("gate", "spot", "k", "s", "", "http://x", false)
	if _, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTCUSDT", Side: "BUY", Qty: 1}); err == nil {
		t.Fatal("want error on unsupported venue, got nil")
	}
}

// TestBinanceSignKnownVector 固定向量验证 binanceSign（HMAC-SHA256 hex）。
func TestBinanceSignKnownVector(t *testing.T) {
	got := binanceSign("secret", "symbol=BTCUSDT")
	// 手工计算预期（固定输入/密钥 → 固定输出）。
	want := hmacHex("secret", "symbol=BTCUSDT")
	if got != want {
		t.Errorf("binanceSign = %q, want %q", got, want)
	}
}

// TestOKXSignKnownVector 固定向量验证 okxSign（base64 HMAC）。
func TestOKXSignKnownVector(t *testing.T) {
	got := okxSign("secret", "2026-08-20T00:00:00.000ZPOST/api/v5/trade/order{}")
	want := base64.StdEncoding.EncodeToString(hmacSHA256("secret", "2026-08-20T00:00:00.000ZPOST/api/v5/trade/order{}"))
	if got != want {
		t.Errorf("okxSign = %q, want %q", got, want)
	}
}

func hmacHex(secret, msg string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

func hmacSHA256(secret, msg string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}
