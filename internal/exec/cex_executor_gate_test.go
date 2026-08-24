package exec

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGateSignKnownVector 固定向量验证 GateSign（HMAC-SHA512 hex，D-098 G1）。
// 手算对拍：sign_string = method\nurl\nquery\npayloadHash\ntimestamp，
// SIGN = hex(HMAC-SHA512(secret, sign_string))。payloadHash = hex(SHA512(body))。
func TestGateSignKnownVector(t *testing.T) {
	const (
		secret = "gate-secret-abc"
		method = "POST"
		url    = "/api/v4/futures/usdt/orders"
		query  = ""
		body   = `{"contract":"BTC_USDT","size":1,"price":"0","tif":"ioc","text":"t-arb"}`
		ts     = int64(1724476800)
	)
	got := GateSign(secret, method, url, query, body, ts)
	// 独立手算预期值（与 GateSign 实现无关，防实现回归）
	payloadHash := hexSHA512(body)
	signString := method + "\n" + url + "\n" + query + "\n" + payloadHash + "\n" + "1724476800"
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write([]byte(signString))
	want := hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Errorf("GateSign =\n  %q\nwant\n  %q", got, want)
	}
}

// TestGateSignEmptyBody GET 无 body 时 payloadHash = hex(SHA512(""))，与有 body 不同。
func TestGateSignEmptyBody(t *testing.T) {
	gotEmpty := GateSign("s", "GET", "/api/v4/futures/usdt/accounts", "", "", 1000)
	gotBody := GateSign("s", "GET", "/api/v4/futures/usdt/accounts", "", "{}", 1000)
	if gotEmpty == gotBody {
		t.Error("空 body 与非空 body 签名应不同（payloadHash 不同）")
	}
	// 空 body 签名应匹配 hex(SHA512("")) 作为 payloadHash
	want := gateSignManual("s", "GET", "/api/v4/futures/usdt/accounts", "", hexSHA512(""), 1000)
	if gotEmpty != want {
		t.Errorf("空 body GateSign = %q, want %q", gotEmpty, want)
	}
}

// gateSignManual 独立计算 Gate 签名（与 GateSign 实现解耦，测试对拍用）。
func gateSignManual(secret, method, urlPath, query, payloadHash string, ts int64) string {
	signString := method + "\n" + urlPath + "\n" + query + "\n" + payloadHash + "\n" + intToStr(ts)
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write([]byte(signString))
	return hex.EncodeToString(mac.Sum(nil))
}

func intToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestCEXExecutor_GateOrderFinished 验证 gate perp 下单：endpoint/签名头/body/全成交响应解析。
func TestCEXExecutor_GateOrderFinished(t *testing.T) {
	const (
		key    = "gate-key-001"
		secret = "gate-secret-xyz"
	)
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/futures/usdt/orders" {
			t.Errorf("want path /futures/usdt/orders, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("want POST, got %s", r.Method)
		}
		// 签名三件 + Content-Type
		if r.Header.Get("KEY") != key {
			t.Errorf("KEY = %q, want %q", r.Header.Get("KEY"), key)
		}
		if r.Header.Get("SIGN") == "" || r.Header.Get("Timestamp") == "" {
			t.Error("missing SIGN or Timestamp header")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		// 签名验证：用请求里的 Timestamp + body 重算
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		ts := r.Header.Get("Timestamp")
		tsInt := parseTs(t, ts)
		wantSign := GateSign(secret, "POST", "/api/v4"+r.URL.Path, "", string(raw), tsInt)
		if r.Header.Get("SIGN") != wantSign {
			t.Errorf("SIGN mismatch: got %q, want %q", r.Header.Get("SIGN"), wantSign)
		}
		// 全成交响应：status=finished, left=0, fill_price
		_, _ = io.WriteString(w, `{"id":"123456","status":"finished","size":1,"left":0,"fill_price":"60000.5"}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("gate", "perp", key, secret, "", srv.URL, true)
	fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC_USDT", Side: "BUY", Qty: 1})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if fill.OrderID != "123456" {
		t.Errorf("OrderID = %q, want 123456", fill.OrderID)
	}
	if fill.Qty != 1 {
		t.Errorf("Qty = %v, want 1", fill.Qty)
	}
	if fill.Price != 60000.5 {
		t.Errorf("Price = %v, want 60000.5", fill.Price)
	}
	// body 应含 contract/size/tif=ioc
	if !strings.Contains(gotBody, `"contract":"BTC_USDT"`) {
		t.Errorf("body 缺 contract: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"size":1`) {
		t.Errorf("body 缺 size:1 (BUY 正多): %s", gotBody)
	}
	if !strings.Contains(gotBody, `"tif":"ioc"`) {
		t.Errorf("body 缺 tif:ioc: %s", gotBody)
	}
}

// TestCEXExecutor_GateOrderSellSizeNegative SELL leg → size 为负（short）。
func TestCEXExecutor_GateOrderSellSizeNegative(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		_, _ = io.WriteString(w, `{"id":"7","status":"finished","size":-2,"left":0,"fill_price":"60000"}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, true)
	if _, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC_USDT", Side: "SELL", Qty: 2}); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if !strings.Contains(gotBody, `"size":-2`) {
		t.Errorf("SELL body 应 size=-2（负=short）: %s", gotBody)
	}
}

// TestCEXExecutor_GateOrderPollsFill status=open（未成交）→ 轮询 GET 拿真实成交
// （对齐 okx/binance 教训，防记 0 qty）。
func TestCEXExecutor_GateOrderPollsFill(t *testing.T) {
	var gotGET bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/futures/usdt/orders":
			_, _ = io.WriteString(w, `{"id":"888","status":"open","size":1,"left":1,"fill_price":"0"}`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/futures/usdt/orders/888"):
			gotGET = true
			_, _ = io.WriteString(w, `{"id":"888","status":"finished","size":1,"left":0,"fill_price":"60123.4"}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, true)
	fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC_USDT", Side: "BUY", Qty: 1})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if !gotGET {
		t.Error("应轮询 GET 订单状态，未轮询")
	}
	if fill.Qty != 1 || fill.Price != 60123.4 {
		t.Errorf("fill = %+v, want Qty 1 Price 60123.4（轮询取真实成交）", fill)
	}
}

// TestCEXExecutor_GateOrderCanceled 轮询发现订单被取消 → 如实报错（非静默返回 0 假成交）。
func TestCEXExecutor_GateOrderCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			_, _ = io.WriteString(w, `{"id":"999","status":"open","size":1,"left":1,"fill_price":"0"}`)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"id":"999","status":"canceled","size":1,"left":1,"fill_price":"0"}`)
		}
	}))
	defer srv.Close()

	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, true)
	_, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC_USDT", Side: "BUY", Qty: 1})
	if err == nil {
		t.Fatal("订单被取消应报错，got nil")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Errorf("错误应含 canceled 状态, got %v", err)
	}
}

// TestCEXExecutor_GateOrderPartialFill status=finished 且 left>0 → 如实返回已成交部分 qty=|size-left|。
func TestCEXExecutor_GateOrderPartialFill(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 直接 finished + left=1（size=5 → 成交 4），不触发轮询（finished 不轮询除非 left!=0... 实际 left!=0 会轮询）
		// 此处测 finished+left!=0 路径：会进入轮询，轮询返回同样部分成交 → 取 qty>0 返回
		switch r.Method {
		case http.MethodPost:
			_, _ = io.WriteString(w, `{"id":"55","status":"finished","size":5,"left":1,"fill_price":"60000"}`)
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"id":"55","status":"finished","size":5,"left":1,"fill_price":"60000"}`)
		}
	}))
	defer srv.Close()

	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, true)
	fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC_USDT", Side: "BUY", Qty: 5})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if fill.Qty != 4 {
		t.Errorf("部分成交 Qty = %v, want 4（|size-left|=|5-1|）", fill.Qty)
	}
}

// TestCEXExecutor_GateSpotUnsupported gate spot（G1 只 perp）→ 如实报错。
func TestCEXExecutor_GateSpotUnsupported(t *testing.T) {
	e := NewCEXExecutor("gate", "spot", "k", "s", "", "http://x", true)
	if _, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC_USDT", Side: "BUY", Qty: 1}); err == nil {
		t.Fatal("gate spot 应报错（G1 仅 perp），got nil")
	}
}

// TestCEXExecutor_GatePositions 验证 GatePositions 解析（多仓 + funding 字段 + 空仓过滤）。
func TestCEXExecutor_GatePositions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/futures/usdt/positions/BTC_USDT" {
			t.Errorf("want /futures/usdt/positions/BTC_USDT, got %s", r.URL.Path)
		}
		if r.Header.Get("KEY") == "" || r.Header.Get("SIGN") == "" {
			t.Error("missing KEY/SIGN header")
		}
		// 返回多仓 + funding；size=0 行应被过滤
		_, _ = io.WriteString(w, `[{"contract":"BTC_USDT","size":3,"entry_price":"60000","mark_price":"60100","funding":"-0.0023"},
{"contract":"BTC_USDT","size":0,"entry_price":"0","mark_price":"0","funding":"0"}]`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, true)
	poss, err := e.GatePositions(context.Background(), "BTC_USDT")
	if err != nil {
		t.Fatalf("GatePositions: %v", err)
	}
	if len(poss) != 1 {
		t.Fatalf("want 1 position（size=0 过滤），got %d", len(poss))
	}
	p := poss[0]
	if p.Size != 3 {
		t.Errorf("Size = %d, want 3", p.Size)
	}
	if p.EntryPrice != 60000 {
		t.Errorf("EntryPrice = %v, want 60000", p.EntryPrice)
	}
	if p.Funding != -0.0023 {
		t.Errorf("Funding = %v, want -0.0023", p.Funding)
	}
}

// TestCEXExecutor_GatePositionsEmpty 空仓（空数组或全 size=0）→ 返回空切片不误报。
func TestCEXExecutor_GatePositionsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[]`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, true)
	poss, err := e.GatePositions(context.Background(), "BTC_USDT")
	if err != nil {
		t.Fatalf("GatePositions: %v", err)
	}
	if len(poss) != 0 {
		t.Errorf("空仓应返回 0 条，got %d", len(poss))
	}
}

// parseTs 解析 Timestamp header 为 int64（测试辅助）。
func parseTs(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("invalid timestamp %q", s)
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
