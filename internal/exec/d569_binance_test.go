package exec

// d569_binance_test.go —— D-569 包A 验收锚 A1-A8（先红后绿+变异对抗）。
// 夹具一律生产形状：base=srv.URL+"/api"（尾段含 /api，exchange_configs 约定），
// 服务端挂 /api/v3/* 路由——双重前缀/裸 /v3 回归皆钉死。
import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// resetSpotFilters 清 spot 过滤器缓存（测试间隔离，包内同文件族全局表）。
func resetSpotFilters() {
	spotFiltersMu.Lock()
	spotFiltersCache = map[string]binanceSpotFilters{}
	spotFiltersMu.Unlock()
	binanceClockOffset.Store(0)
}

// d569Fixture 生产形状 Binance spot 夹具：baseURL=srv+"/api"；路由表按请求注入。
type d569Fixture struct {
	srv *httptest.Server
	e   *CEXExecutor
	// 计数器（原子）：按路径首段计请求数。
	orderCalls, tickerCalls, exchInfoCalls, timeCalls atomic.Int32
	lastQuery                                         atomic.Value // map[string]string（最近一次 /v3/order query）
	tsSeen                                            []int64      // order 请求 timestamp 序列（A7 校时断言）
	mu                                                sync.Mutex
	handlers                                          map[string]func(w http.ResponseWriter, r *http.Request)
}

func newD569Fixture(t *testing.T, symbolFilters string, orderResp func(call int) (int, string)) *d569Fixture {
	f := &d569Fixture{handlers: map[string]func(http.ResponseWriter, *http.Request){}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/exchangeInfo":
			f.exchInfoCalls.Add(1)
			_, _ = io.WriteString(w, `{"symbols":[{"symbol":"XAUTUSDT","filters":`+symbolFilters+`}]}`)
		case r.URL.Path == "/api/v3/ticker/price":
			f.tickerCalls.Add(1)
			if h, ok := f.handlers["ticker"]; ok {
				h(w, r)
				return
			}
			_, _ = io.WriteString(w, `{"price":"3000.0"}`)
		case r.URL.Path == "/api/v3/time":
			f.timeCalls.Add(1)
			if h, ok := f.handlers["time"]; ok {
				h(w, r)
				return
			}
			fmt.Fprintf(w, `{"serverTime":%d}`, time.Now().UnixMilli())
		case r.URL.Path == "/api/v3/order":
			n := f.orderCalls.Add(1)
			f.mu.Lock()
			q := map[string]string{}
			for k := range r.URL.Query() {
				q[k] = r.URL.Query().Get(k)
			}
			f.lastQuery.Store(q)
			if ts, _ := strconv.ParseInt(q["timestamp"], 10, 64); ts > 0 {
				f.tsSeen = append(f.tsSeen, ts)
			}
			f.mu.Unlock()
			if h, ok := f.handlers["order"]; ok {
				h(w, r)
				return
			}
			code, body := orderResp(int(n))
			w.WriteHeader(code)
			_, _ = io.WriteString(w, body)
		default:
			http.NotFound(w, r)
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	f.e = NewCEXExecutor("binance", "spot", "k", "s", "", f.srv.URL+"/api", false)
	return f
}

const d569LotPass = `[{"filterType":"LOT_SIZE","minQty":"0.0001","maxQty":"9000","stepSize":"0.0001"}]`
const d569OrderOK = `{"symbol":"XAUTUSDT","orderId":1,"executedQty":"0.0001","avgPrice":"3000","status":"FILLED"}`

// A1：生产形状回归——base 带 /api 尾段 → 服务端收 POST /api/v3/order（双重前缀红）。
func TestD569_A1_EndpointsProdShape(t *testing.T) {
	resetSpotFilters()
	f := newD569Fixture(t, d569LotPass, func(int) (int, string) { return 200, d569OrderOK })
	if _, err := f.e.PlaceOrder(context.Background(), Order{Symbol: "XAUTUSDT", Side: "BUY", Qty: 0.0001}); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if f.orderCalls.Load() != 1 {
		t.Fatalf("want 1 order call, got %d", f.orderCalls.Load())
	}
	// 六端点逐一断言 /v3/* 端点串（机械锚——任一回归 /api/v3 即红）。
	// 每唯一路径注册一次命中记录 handler（Go ServeMux 同 pattern 重复注册 panic）。
	hits := map[string]*atomic.Int32{}
	mux := f.srv.Config.Handler.(*http.ServeMux)
	for _, p := range []string{"/api/v3/order", "/api/v3/myTrades", "/api/v3/ticker/price", "/api/v3/account"} {
		c := &atomic.Int32{}
		hits[p] = c
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			c.Add(1)
			w.WriteHeader(200)
			_, _ = io.WriteString(w, `{}`)
		})
	}
	for _, tc := range []struct {
		name string
		call func() error
		want string
	}{
		{"orderStatus", func() error { _, e := f.e.binanceOrderStatus(context.Background(), "XAUTUSDT", "1", ""); return e }, "/api/v3/order"},
		{"cancel", func() error { return f.e.binanceCancelOrder(context.Background(), "XAUTUSDT", "1") }, "/api/v3/order"},
		{"myTrades", func() error { _, e := f.e.binanceUserTradesFeeUSD(context.Background(), "XAUTUSDT", "1"); return e }, "/api/v3/myTrades"},
		{"ticker", func() error { _, e := f.e.spotMarkPrice(context.Background(), "XAUTUSDT"); return e }, "/api/v3/ticker/price"},
		{"account", func() error { _, e := f.e.spotQuoteAvail(context.Background(), "USDT"); return e }, "/api/v3/account"},
	} {
		_ = tc.call() // 响应体不符无妨——本锚只钉路径形状
		if hits[tc.want].Load() == 0 {
			t.Errorf("%s: want %s hit", tc.name, tc.want)
		}
	}
	// exchangeInfo 已在上列请求路径中间接命中；显式断言 guard 拉取路径。
	if f.exchInfoCalls.Load() == 0 {
		t.Fatal("exchangeInfo never fetched")
	}
}

// A2：floor 到 stepSize 输出浮点干净值（'f',-1 非 17 位伪影）+ minQty 拒单含两值。
func TestD569_A2_FloorCleanMinQty(t *testing.T) {
	resetSpotFilters()
	f := newD569Fixture(t, d569LotPass, func(int) (int, string) { return 200, d569OrderOK })
	if _, err := f.e.PlaceOrder(context.Background(), Order{Symbol: "XAUTUSDT", Side: "BUY", Qty: 0.000123}); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	q := f.lastQuery.Load().(map[string]string)
	if q["quantity"] != "0.0001" {
		t.Fatalf("want quantity=0.0001 clean, got %q", q["quantity"])
	}
	// minQty 拒：qty=0.00001 floor 后 < minQty=0.0001 → 错误含 minQty 与两值且服务端零下单。
	if _, err := f.e.PlaceOrder(context.Background(), Order{Symbol: "XAUTUSDT", Side: "BUY", Qty: 0.00001}); err == nil ||
		!strings.Contains(err.Error(), "minQty 0.0001") {
		t.Fatalf("want minQty reject, got %v", err)
	}
	if f.orderCalls.Load() != 1 {
		t.Fatalf("reject must not reach venue, order calls=%d", f.orderCalls.Load())
	}
}

// A3：NOTIONAL 双形——minNotional=5 qty*last=3 拒；旧形 MIN_NOTIONAL 同拒；
// applyMin=false 不卡 min、applyMax=false 不卡 max（实拍形状）。
func TestD569_A3_NotionalDualShape(t *testing.T) {
	// 新形：applyMinToMarket=true + minNotional=5（qty=0.001×3000=3 <5 拒）
	resetSpotFilters()
	filters := `[{"filterType":"LOT_SIZE","minQty":"0.0001","maxQty":"9000","stepSize":"0.001"},
		{"filterType":"NOTIONAL","minNotional":"5","maxNotional":"9000000","applyMinToMarket":true,"applyMaxToMarket":false}]`
	f := newD569Fixture(t, filters, func(int) (int, string) { return 200, d569OrderOK })
	if _, err := f.e.PlaceOrder(context.Background(), Order{Symbol: "XAUTUSDT", Side: "BUY", Qty: 0.001}); err == nil || !strings.Contains(err.Error(), "minNotional") {
		t.Fatalf("want notional reject, got %v", err)
	}
	// 旧形 MIN_NOTIONAL（applyMin 视为 true）同拒。
	resetSpotFilters()
	f2 := newD569Fixture(t, `[{"filterType":"LOT_SIZE","minQty":"0.0001","maxQty":"9000","stepSize":"0.001"},
		{"filterType":"MIN_NOTIONAL","minNotional":"5"}]`, func(int) (int, string) { return 200, d569OrderOK })
	if _, err := f2.e.PlaceOrder(context.Background(), Order{Symbol: "XAUTUSDT", Side: "BUY", Qty: 0.001}); err == nil || !strings.Contains(err.Error(), "minNotional") {
		t.Fatalf("legacy MIN_NOTIONAL want reject, got %v", err)
	}
	// applyMin=false + applyMax=false：minNotional/maxNotional 均不卡（qty*last=3<5 与 >max 皆放行——max 用例 A5 计数互补）。
	resetSpotFilters()
	f3 := newD569Fixture(t, `[{"filterType":"LOT_SIZE","minQty":"0.0001","maxQty":"9000","stepSize":"0.001"},
		{"filterType":"NOTIONAL","minNotional":"5","maxNotional":"1","applyMinToMarket":false,"applyMaxToMarket":false}]`,
		func(int) (int, string) { return 200, d569OrderOK })
	if _, err := f3.e.PlaceOrder(context.Background(), Order{Symbol: "XAUTUSDT", Side: "BUY", Qty: 0.001}); err != nil {
		t.Fatalf("both apply=false must pass, got %v", err)
	}
}

// A4：量上限——mktMaxQty=446.98 qty=500 拒含 MARKET_LOT_SIZE 值；lotMaxQty 同。
func TestD569_A4_MaxQtyGuards(t *testing.T) {
	resetSpotFilters()
	f := newD569Fixture(t, `[{"filterType":"LOT_SIZE","minQty":"0.0001","maxQty":"446.98","stepSize":"0.01"},
		{"filterType":"MARKET_LOT_SIZE","maxQty":"446.98"}]`, func(int) (int, string) { return 200, d569OrderOK })
	if _, err := f.e.PlaceOrder(context.Background(), Order{Symbol: "XAUTUSDT", Side: "BUY", Qty: 500}); err == nil ||
		!strings.Contains(err.Error(), "446.98") {
		t.Fatalf("want mktMaxQty reject, got %v", err)
	}
	if f.orderCalls.Load() != 0 {
		t.Fatalf("reject must not reach venue, calls=%d", f.orderCalls.Load())
	}
	// LOT_SIZE.maxQty 独立触发（mkt 上限更大时 lot 上限仍拦）。
	resetSpotFilters()
	f2 := newD569Fixture(t, `[{"filterType":"LOT_SIZE","minQty":"0.0001","maxQty":"100","stepSize":"0.01"},
		{"filterType":"MARKET_LOT_SIZE","maxQty":"9000"}]`, func(int) (int, string) { return 200, d569OrderOK })
	if _, err := f2.e.PlaceOrder(context.Background(), Order{Symbol: "XAUTUSDT", Side: "BUY", Qty: 200}); err == nil ||
		!strings.Contains(err.Error(), "LOT_SIZE") {
		t.Fatalf("want lotMaxQty reject, got %v", err)
	}
}

// A5：条件取价 fail-closed——名义闸生效+ticker 500 → 拒含取败；
// 两 apply 皆 false → ticker 故障不影响下单且 GET 计数=0（R1 F-3 钉死）。
func TestD569_A5_ConditionalPricing(t *testing.T) {
	resetSpotFilters()
	f := newD569Fixture(t, `[{"filterType":"LOT_SIZE","minQty":"0.0001","maxQty":"9000","stepSize":"0.001"},
		{"filterType":"NOTIONAL","minNotional":"5","applyMinToMarket":true,"applyMaxToMarket":false}]`,
		func(int) (int, string) { return 200, d569OrderOK })
	f.handlers["ticker"] = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }
	if _, err := f.e.PlaceOrder(context.Background(), Order{Symbol: "XAUTUSDT", Side: "BUY", Qty: 0.01}); err == nil ||
		!strings.Contains(err.Error(), "取价") {
		t.Fatalf("want fail-closed pricing reject, got %v", err)
	}
	// 两 apply 皆 false：ticker 故障 + GET 计数=0。
	resetSpotFilters()
	f2 := newD569Fixture(t, `[{"filterType":"LOT_SIZE","minQty":"0.0001","maxQty":"9000","stepSize":"0.001"},
		{"filterType":"NOTIONAL","minNotional":"5","maxNotional":"1","applyMinToMarket":false,"applyMaxToMarket":false}]`,
		func(int) (int, string) { return 200, d569OrderOK })
	f2.handlers["ticker"] = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }
	if _, err := f2.e.PlaceOrder(context.Background(), Order{Symbol: "XAUTUSDT", Side: "BUY", Qty: 0.01}); err != nil {
		t.Fatalf("apply=false must skip pricing, got %v", err)
	}
	if n := f2.tickerCalls.Load(); n != 0 {
		t.Fatalf("want 0 ticker GET when both apply false, got %d", n)
	}
}

// A6：非 200 → errors.As(*httpStatusError) 命中；Error() 与旧 "http %d: %s" 逐字一致。
func TestD569_A6_TypedHTTPError(t *testing.T) {
	resetSpotFilters()
	f := newD569Fixture(t, d569LotPass, func(int) (int, string) { return 400, `{"code":-1100,"msg":"bad"}` })
	_, err := f.e.binanceOrderStatus(context.Background(), "XAUTUSDT", "1", "")
	var hse *httpStatusError
	if !errors.As(err, &hse) {
		t.Fatalf("want *httpStatusError, got %T %v", err, err)
	}
	want := fmt.Sprintf("http %d: %s", 400, `{"code":-1100,"msg":"bad"}`)
	if err.Error() != want {
		t.Fatalf("Error() drift: got %q want %q", err.Error(), want)
	}
}

// A7：-1021 校时——首签 -1021 → /v3/time 返 serverTime≈local+60s → 同 query 重签重发成功；
// 第二请求 timestamp 偏移≈60s；恒 -1021 → 重发=1 次后上抛。
func TestD569_A7_Minus1021ClockSync(t *testing.T) {
	resetSpotFilters()
	var orderSeq atomic.Int32
	f := newD569Fixture(t, d569LotPass, func(int) (int, string) { return 200, d569OrderOK })
	f.handlers["time"] = func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"serverTime":%d}`, time.Now().UnixMilli()+60000)
	}
	f.handlers["order"] = func(w http.ResponseWriter, r *http.Request) {
		if orderSeq.Add(1) == 1 {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"code":-1021,"msg":"Timestamp for this request is outside of the recvWindow."}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"FILLED","executedQty":"0.1","avgPrice":"1"}`)
	}
	res, err := f.e.binanceOrderStatus(context.Background(), "XAUTUSDT", "1", "")
	if err != nil || res.Status != "FILLED" {
		t.Fatalf("want resend success, got %v %+v", err, res)
	}
	if len(f.tsSeen) != 2 {
		t.Fatalf("want 2 signed attempts, got %d", len(f.tsSeen))
	}
	if d := f.tsSeen[1] - f.tsSeen[0]; d < 55000 || d > 65000 {
		t.Fatalf("want ts2-ts1≈60s, got %d ms", d)
	}
	// 恒 -1021：校时后重发仍 -1021 → 上抛；order 计数=2（首发+重发一），time 计数=1。
	resetSpotFilters()
	var orderSeq2 atomic.Int32
	f2 := newD569Fixture(t, d569LotPass, func(int) (int, string) { return 200, d569OrderOK })
	f2.handlers["time"] = func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"serverTime":%d}`, time.Now().UnixMilli()+60000)
	}
	f2.handlers["order"] = func(w http.ResponseWriter, r *http.Request) {
		orderSeq2.Add(1)
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"code":-1021,"msg":"again"}`)
	}
	if _, err := f2.e.binanceOrderStatus(context.Background(), "XAUTUSDT", "1", ""); err == nil {
		t.Fatal("persistent -1021 must propagate")
	}
	if n := orderSeq2.Load(); n != 2 {
		t.Fatalf("want exactly 1 resend (2 attempts), got %d", n)
	}
	if n := f2.timeCalls.Load(); n != 1 {
		t.Fatalf("want 1 clock sync, got %d", n)
	}
}

// A8：GET 退避——-1003×2 后成功（binanceOrderStatus 计数=3）；418 不重试直接上抛（计数=1）。
func TestD569_A8_GetBackoff(t *testing.T) {
	resetSpotFilters()
	var seq atomic.Int32
	f := newD569Fixture(t, d569LotPass, func(int) (int, string) { return 200, d569OrderOK })
	f.handlers["order"] = func(w http.ResponseWriter, r *http.Request) {
		if seq.Add(1) <= 2 {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"code":-1003,"msg":"Too many requests"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"FILLED","executedQty":"0.1","avgPrice":"1"}`)
	}
	res, err := f.e.binanceOrderStatus(context.Background(), "XAUTUSDT", "1", "")
	if err != nil || res.Status != "FILLED" {
		t.Fatalf("want 3rd attempt success, got %v %+v", err, res)
	}
	if n := seq.Load(); n != 3 {
		t.Fatalf("want 3 attempts, got %d", n)
	}
	// 418：IP 封禁不重试。
	resetSpotFilters()
	var seq2 atomic.Int32
	f2 := newD569Fixture(t, d569LotPass, func(int) (int, string) { return 200, d569OrderOK })
	f2.handlers["order"] = func(w http.ResponseWriter, r *http.Request) {
		seq2.Add(1)
		w.WriteHeader(418)
		_, _ = io.WriteString(w, `{"code":-1003,"msg":"banned"}`)
	}
	if _, err := f2.e.binanceOrderStatus(context.Background(), "XAUTUSDT", "1", ""); err == nil {
		t.Fatal("418 must propagate")
	}
	if n := seq2.Load(); n != 1 {
		t.Fatalf("418 must not retry, got %d attempts", n)
	}
}
