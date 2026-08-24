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
		// 全成交响应：id=int64(number), status=finished, finish_as=filled, left=0, fill_price（M1/M2 对拍官方 SDK）
		_, _ = io.WriteString(w, `{"id":123456,"status":"finished","finish_as":"filled","size":1,"left":0,"fill_price":"60000.5"}`)
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
		_, _ = io.WriteString(w, `{"id":7,"status":"finished","finish_as":"filled","size":-2,"left":0,"fill_price":"60000"}`)
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
// （对齐 okx/binance 教训，防记 0 qty）。M2：终态按 finish_as 判定。
func TestCEXExecutor_GateOrderPollsFill(t *testing.T) {
	var gotGET bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/futures/usdt/orders":
			_, _ = io.WriteString(w, `{"id":888,"status":"open","size":1,"left":1,"fill_price":"0"}`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/futures/usdt/orders/888"):
			gotGET = true
			_, _ = io.WriteString(w, `{"id":888,"status":"finished","finish_as":"filled","size":1,"left":0,"fill_price":"60123.4"}`)
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

// TestCEXExecutor_GateOrderZeroFillIOC 零成交 IOC（M2 新增 case ①）：POST 即 finished +
// finish_as=ioc + left=size → 必须 error 非 0-qty 假 Fill（okx 教训复现防护）。
// gate status 无 canceled/rejected；取消/IOC 未成交原因在 finish_as。
func TestCEXExecutor_GateOrderZeroFillIOC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// IOC 市价单无对手盘 → 立即 finished + finish_as=ioc + left=size（零成交）
		_, _ = io.WriteString(w, `{"id":999,"status":"finished","finish_as":"ioc","size":1,"left":1,"fill_price":"0"}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, true)
	fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC_USDT", Side: "BUY", Qty: 1})
	if err == nil {
		t.Fatalf("零成交 IOC 应报错，got Fill=%+v（okx 教训：防记 0 qty 假成交）", fill)
	}
	if !strings.Contains(err.Error(), "ioc") {
		t.Errorf("错误应含 finish_as=ioc, got %v", err)
	}
	if fill.Qty != 0 {
		t.Errorf("零成交 Fill.Qty 应 0, got %v", fill.Qty)
	}
}

// TestCEXExecutor_GateOrderPartialFillIOC 部分成交 finish_as=ioc qty>0（M2 新增 case ②）：
// POST finished + finish_as=ioc + left=1（size=5 → 成交 4）→ 部分 Fill qty=4（如实，非 0-qty 假成交）。
func TestCEXExecutor_GateOrderPartialFillIOC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// IOC 部分成交：finish_as=ioc，size=5 left=1 → 成交 4
		_, _ = io.WriteString(w, `{"id":55,"status":"finished","finish_as":"ioc","size":5,"left":1,"fill_price":"60000"}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, true)
	fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC_USDT", Side: "BUY", Qty: 5})
	if err != nil {
		t.Fatalf("部分成交不应报错: %v", err)
	}
	if fill.Qty != 4 {
		t.Errorf("部分成交 Qty = %v, want 4（|size-left|=|5-1|）", fill.Qty)
	}
	if fill.Price != 60000 {
		t.Errorf("部分成交 Price = %v, want 60000", fill.Price)
	}
}

// TestCEXExecutor_GateSpotUnsupported gate spot（G1 只 perp）→ 如实报错。
func TestCEXExecutor_GateSpotUnsupported(t *testing.T) {
	e := NewCEXExecutor("gate", "spot", "k", "s", "", "http://x", true)
	if _, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTC_USDT", Side: "BUY", Qty: 1}); err == nil {
		t.Fatal("gate spot 应报错（G1 仅 perp），got nil")
	}
}

// TestCEXExecutor_GatePositions 验证 GatePositions 解析（M3：单对象非数组，对拍 gateapi-go GetPosition）。
// 多仓 + funding 字段；size!=0 → 返回 1 条。
func TestCEXExecutor_GatePositions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/futures/usdt/positions/BTC_USDT" {
			t.Errorf("want /futures/usdt/positions/BTC_USDT, got %s", r.URL.Path)
		}
		if r.Header.Get("KEY") == "" || r.Header.Get("SIGN") == "" {
			t.Error("missing KEY/SIGN header")
		}
		// M3：GetPosition 返回单个 Position 对象（非数组，ListPositions 才是数组）
		_, _ = io.WriteString(w, `{"contract":"BTC_USDT","size":3,"entry_price":"60000","mark_price":"60100","funding":"-0.0023"}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, true)
	poss, err := e.GatePositions(context.Background(), "BTC_USDT")
	if err != nil {
		t.Fatalf("GatePositions: %v", err)
	}
	if len(poss) != 1 {
		t.Fatalf("want 1 position，got %d", len(poss))
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

// TestCEXExecutor_GatePositionsEmpty 空仓（M3：单对象 size=0）→ 返回空切片不误报。
func TestCEXExecutor_GatePositionsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 无仓时 gate 返回 size=0 的单对象（非空数组）
		_, _ = io.WriteString(w, `{"contract":"BTC_USDT","size":0,"entry_price":"0","mark_price":"0","funding":"0"}`)
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

// TestCEXExecutor_GatePositionsRaw 验证 GatePositionsRaw 返回原始 body + 解析（D-098 返修② 探针形状证据）。
// 原始 body 必须含 funding 字段（探针一次运行定案字段是否存在）。
func TestCEXExecutor_GatePositionsRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/futures/usdt/positions/BTC_USDT" {
			t.Errorf("want /futures/usdt/positions/BTC_USDT, got %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"contract":"BTC_USDT","size":2,"entry_price":"60000","mark_price":"60100","funding":"-0.001"}`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, true)
	raw, poss, err := e.GatePositionsRaw(context.Background(), "BTC_USDT")
	if err != nil {
		t.Fatalf("GatePositionsRaw: %v", err)
	}
	if !strings.Contains(string(raw), `"funding"`) {
		t.Errorf("原始 body 应含 funding 字段（探针形状证据），got %s", string(raw))
	}
	if len(poss) != 1 || poss[0].Funding != -0.001 {
		t.Errorf("解析错误：poss=%+v", poss)
	}
}

// TestCEXExecutor_GateAccountBookFund 验证 account_book?type=fund 签名 GET + 数组解析（D-098 返修② 结算真实性证据）。
// 对拍：query=type=fund&contract={c} 进签名（GateSign 第三段），URL 含 query，响应=数组。
func TestCEXExecutor_GateAccountBookFund(t *testing.T) {
	var gotQuery, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		// 独立验证签名（query 进 sign_string 第三段）
		ts := parseTs(t, r.Header.Get("Timestamp"))
		wantSign := GateSign("s", "GET", "/api/v4/futures/usdt/account_book", gotQuery, "", ts)
		if r.Header.Get("SIGN") != wantSign {
			t.Errorf("SIGN query 签名不匹配：got %s want %s", r.Header.Get("SIGN"), wantSign)
		}
		_, _ = io.WriteString(w, `[{"time":1724476800,"change":"-0.00123","refid":"r1","text":"funding"},{"time":1724480600,"change":"0.00045","refid":"r2","text":"funding"}]`)
	}))
	defer srv.Close()

	e := NewCEXExecutor("gate", "perp", "k", "s", "", srv.URL, true)
	raw, entries, err := e.GateAccountBookFund(context.Background(), "BTC_USDT")
	if err != nil {
		t.Fatalf("GateAccountBookFund: %v", err)
	}
	if gotPath != "/futures/usdt/account_book" {
		t.Errorf("path = %s, want /futures/usdt/account_book", gotPath)
	}
	if gotQuery != "type=fund&contract=BTC_USDT" {
		t.Errorf("query = %s, want type=fund&contract=BTC_USDT", gotQuery)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	if entries[0].Text != "funding" || entries[0].Change != "-0.00123" || entries[0].Time != 1724476800 {
		t.Errorf("entry[0] = %+v", entries[0])
	}
	if !strings.Contains(string(raw), `"text":"funding"`) {
		t.Errorf("原始 body 应含 text=funding（结算行证据），got %s", string(raw))
	}
}
