package exec

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 验收锚 A1-A6（真形 httptest 夹具；instID 用独立符号防包级规格缓存串扰）。

const d566InstrumentsPerp = `{"code":"0","data":[{"ctVal":"1","lotSz":"1","maxMktSz":"%s","state":"live","instId":"%s"}]}`

// d566PerpSrv perp 通用夹具：instruments(maxMktSz 可调) + account/config + POST order。
func d566PerpSrv(t *testing.T, instID, maxMktSz string, postFn func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/public/instruments":
			_, _ = io.WriteString(w, fmt.Sprintf(d566InstrumentsPerp, maxMktSz, instID))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/account/config":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"posMode":"long_short_mode"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/order":
			if postFn != nil {
				postFn(w, r)
				return
			}
			_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"1","sCode":"0","accFillSz":"1","avgPx":"100"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/trade/order":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"0","fee":"","feeCcy":""}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
}

// A1：perp maxMktSz=100，qty 150 张→拒单含 "maxMktSz"；≤100 正常路径计数不变。
func TestD566_A1_PerpMaxMktSzGate(t *testing.T) {
	instID := "D566A-USDT-SWAP"
	var posts atomic.Int32
	srv := d566PerpSrv(t, instID, "100", func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"1","sCode":"0","accFillSz":"50","avgPx":"100"}]}`)
	})
	defer srv.Close()
	e := NewCEXExecutor("okx", "perp", "k", "s", "p", srv.URL, true)

	_, err := e.PlaceOrder(context.Background(), Order{Symbol: instID, Side: "BUY", Qty: 150})
	if err == nil || !strings.Contains(err.Error(), "maxMktSz") {
		t.Fatalf("qty 150 张 > maxMktSz 100 want reject含maxMktSz, got %v", err)
	}
	if posts.Load() != 0 {
		t.Fatalf("拒单不应发出 POST, got %d", posts.Load())
	}
	// ≤100 正常路径不受扰
	f, err := e.PlaceOrder(context.Background(), Order{Symbol: instID, Side: "BUY", Qty: 50})
	if err != nil {
		t.Fatalf("qty 50 want ok, got %v", err)
	}
	if f.Qty != 50 || posts.Load() != 1 {
		t.Fatalf("fill=%+v posts=%d", f, posts.Load())
	}
}

// A2：spot maxMktAmt=1000 + ticker last=100，qty 20→名义 2000>1000 拒单含名义值；
// maxMktAmt=0 时 ticker 请求计数=0。
func TestD566_A2_SpotMaxMktAmtGate(t *testing.T) {
	instID := "D566B-USDT"
	var tickerCalls atomic.Int32
	mkSrv := func(maxMktAmt string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/v5/public/instruments":
				_, _ = io.WriteString(w, fmt.Sprintf(`{"code":"0","data":[{"lotSz":"1","minSz":"1","maxMktAmt":"%s","instId":"%s"}]}`, maxMktAmt, instID))
			case r.Method == http.MethodGet && r.URL.Path == "/api/v5/market/ticker":
				tickerCalls.Add(1)
				_, _ = io.WriteString(w, `{"code":"0","data":[{"last":"100"}]}`)
			case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/order":
				_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"1","sCode":"0","accFillSz":"20","avgPx":"100"}]}`)
			case r.Method == http.MethodGet && r.URL.Path == "/api/v5/trade/order":
				_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"0","fee":"","feeCcy":""}]}`)
			default:
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			}
		}))
	}

	srv := mkSrv("1000")
	e := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, true)
	_, err := e.PlaceOrder(context.Background(), Order{Symbol: instID, Side: "BUY", Qty: 20})
	if err == nil || !strings.Contains(err.Error(), "2000") {
		t.Fatalf("qty20×last100=2000 > maxMktAmt 1000 want reject含名义值, got %v", err)
	}
	srv.Close()

	// maxMktAmt=0 → 跳过 ticker（零额外开销）。换独立符号防缓存。
	instID2 := "D566C-USDT"
	var t2calls atomic.Int32
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/public/instruments":
			_, _ = io.WriteString(w, fmt.Sprintf(`{"code":"0","data":[{"lotSz":"1","minSz":"1","instId":"%s"}]}`, instID2))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/market/ticker":
			t2calls.Add(1)
			_, _ = io.WriteString(w, `{"code":"0","data":[{"last":"100"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/order":
			_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"1","sCode":"0","accFillSz":"20","avgPx":"100"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/trade/order":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"0","fee":"","feeCcy":""}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv2.Close()
	e2 := NewCEXExecutor("okx", "spot", "k", "s", "p", srv2.URL, true)
	if _, err := e2.PlaceOrder(context.Background(), Order{Symbol: instID2, Side: "BUY", Qty: 20}); err != nil {
		t.Fatalf("maxMktAmt=0 want ok, got %v", err)
	}
	if t2calls.Load() != 0 {
		t.Fatalf("maxMktAmt=0 want ticker 请求=0, got %d", t2calls.Load())
	}
}

// A3：spot body 含 "banAmend":true；perp body 不含该字段。
func TestD566_A3_SpotBanAmend(t *testing.T) {
	var spotBody, perpBody atomic.Value
	srvSpot := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v5/public/instruments":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"lotSz":"1","minSz":"1"}]}`)
		case r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			spotBody.Store(string(raw))
			_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"1","sCode":"0","accFillSz":"1","avgPx":"1"}]}`)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"0","fee":"","feeCcy":""}]}`)
		}
	}))
	defer srvSpot.Close()
	e := NewCEXExecutor("okx", "spot", "k", "s", "p", srvSpot.URL, true)
	if _, err := e.PlaceOrder(context.Background(), Order{Symbol: "D566D-USDT", Side: "BUY", Qty: 1}); err != nil {
		t.Fatalf("spot order: %v", err)
	}
	if b := spotBody.Load().(string); !strings.Contains(b, `"banAmend":true`) {
		t.Fatalf("spot body want banAmend:true, got %s", b)
	}

	instID := "D566E-USDT-SWAP"
	srvPerp := d566PerpSrv(t, instID, "", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		perpBody.Store(string(raw))
		_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"1","sCode":"0","accFillSz":"1","avgPx":"1"}]}`)
	})
	defer srvPerp.Close()
	ep := NewCEXExecutor("okx", "perp", "k", "s", "p", srvPerp.URL, true)
	if _, err := ep.PlaceOrder(context.Background(), Order{Symbol: instID, Side: "BUY", Qty: 1}); err != nil {
		t.Fatalf("perp order: %v", err)
	}
	if b := perpBody.Load().(string); strings.Contains(b, "banAmend") {
		t.Fatalf("perp body 不应含 banAmend, got %s", b)
	}
}

// A4：POST order 头 expTime≈now+15s（±2s 容差）；cancel-order 无 expTime。
func TestD566_A4_ExpTimeHeader(t *testing.T) {
	var gotExp atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v5/public/instruments":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"lotSz":"1","minSz":"1"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/order":
			v, _ := strconv.ParseInt(r.Header.Get("expTime"), 10, 64)
			gotExp.Store(v)
			_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"1","sCode":"0","accFillSz":"1","avgPx":"1"}]}`)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"0","fee":"","feeCcy":""}]}`)
		}
	}))
	defer srv.Close()
	e := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, true)
	before := time.Now().UnixMilli()
	if _, err := e.PlaceOrder(context.Background(), Order{Symbol: "D566F-USDT", Side: "BUY", Qty: 1}); err != nil {
		t.Fatalf("order: %v", err)
	}
	exp := gotExp.Load()
	if exp == 0 {
		t.Fatal("POST order 缺 expTime 头")
	}
	if exp < before+13_000 || exp > time.Now().UnixMilli()+17_000 {
		t.Fatalf("expTime=%d want ≈now+15s（±2s）, now=%d", exp, before)
	}
	// cancel-order 无 expTime（官方支持列表不含撤单）
	var cancelExp atomic.Bool
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("expTime") != "" {
			cancelExp.Store(true)
		}
		_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"1"}]}`)
	}))
	defer srv2.Close()
	e2 := NewCEXExecutor("okx", "spot", "k", "s", "p", srv2.URL, true)
	if err := e2.okxCancelOrder(context.Background(), "BTC-USDT", "1"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelExp.Load() {
		t.Fatal("cancel-order 不应带 expTime")
	}
}

// A5：fake 首响应 HTTP 401+{"code":"50102"} → 校时后重发成功；第二请求 ts 已偏移
// （fake 时差 +60s）；httpStatusError.Error() 与旧 fmt.Errorf 消息逐字一致。
func TestD566_A5_ClockResync50102(t *testing.T) {
	// 消息逐字一致断言（既有调用方零扰动）
	hse := &httpStatusError{status: 401, body: []byte(`  {"code":"50102"} `)}
	want := fmt.Errorf("http %d: %s", 401, strings.TrimSpace(`{"code":"50102"}`)).Error()
	if hse.Error() != want {
		t.Fatalf("httpStatusError.Error()=%q want %q", hse.Error(), want)
	}

	instID := "D566G-USDT"
	var posts atomic.Int32
	var tsSeen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v5/public/instruments":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"lotSz":"1","minSz":"1"}]}`)
		case r.URL.Path == "/api/v5/public/time":
			// fake 服务器时钟 = 本地 +60s
			_, _ = io.WriteString(w, fmt.Sprintf(`{"code":"0","data":[{"ts":"%d"}]}`, time.Now().Add(60*time.Second).UnixMilli()))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/order":
			n := posts.Add(1)
			tsSeen = append(tsSeen, r.Header.Get("OK-ACCESS-TIMESTAMP"))
			if n == 1 {
				w.WriteHeader(401)
				_, _ = io.WriteString(w, `{"code":"50102","msg":"Timestamp request expired"}`)
				return
			}
			_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"1","sCode":"0","accFillSz":"1","avgPx":"1"}]}`)
		case r.Method == http.MethodGet:
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"0","fee":"","feeCcy":""}]}`)
		}
	}))
	defer srv.Close()
	e := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, true)
	if _, err := e.PlaceOrder(context.Background(), Order{Symbol: instID, Side: "BUY", Qty: 1}); err != nil {
		t.Fatalf("50102 校时后重发应成功, got %v", err)
	}
	if posts.Load() != 2 {
		t.Fatalf("want 2 POSTs（首发401→重发）, got %d", posts.Load())
	}
	if len(tsSeen) == 2 {
		t0, e0 := time.Parse("2006-01-02T15:04:05.000Z", tsSeen[0])
		t1, e1 := time.Parse("2006-01-02T15:04:05.000Z", tsSeen[1])
		if e0 != nil || e1 != nil || t1.Sub(t0) < 50*time.Second {
			t.Fatalf("重发 ts 应已偏移 ≈+60s, got %q vs %q (err %v/%v)", tsSeen[0], tsSeen[1], e0, e1)
		}
	}
	if d := e.clockDeltaMs.Load(); d < 55_000 || d > 65_000 {
		t.Fatalf("clockDeltaMs=%d want ≈60000", d)
	}
}

// A6：okxOrderStatus 遇 50011×2 → 退避后第三连成功（GET 计数=3）。
func TestD566_A6_GETBackoff(t *testing.T) {
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := gets.Add(1)
		if n <= 2 {
			_, _ = io.WriteString(w, `{"code":"50011","msg":"Too many requests","data":[]}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"1","state":"filled","accFillSz":"1","avgPx":"100","fee":"","feeCcy":""}]}`)
	}))
	defer srv.Close()
	e := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, true)
	st, err := e.okxOrderStatus(context.Background(), "BTC-USDT", "1", "")
	if err != nil {
		t.Fatalf("退避后第三连应成功, got %v", err)
	}
	if st.State != "filled" {
		t.Fatalf("state=%q", st.State)
	}
	if gets.Load() != 3 {
		t.Fatalf("GET 计数 want 3, got %d", gets.Load())
	}
}
