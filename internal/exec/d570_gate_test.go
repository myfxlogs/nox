package exec

// d570_gate_test.go —— D-570 包A 验收锚 A1-A6（先红后绿+变异对抗）。
// 夹具生产形状：base=srv.URL+"/api/v4"，路由挂 /api/v4/futures/usdt/*；
// spec 夹具 leverage_max/market_order_size_max 用实弹字符串形（api-response-fields-empirical）。
import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// d570Fixture Gate perp 夹具：handlers 按 "METHOD path" 或 path 注入。
type d570Fixture struct {
	srv         *httptest.Server
	e           *CEXExecutor
	orderCalls  atomic.Int32
	statusCalls atomic.Int32
	tickerCalls atomic.Int32
	lastBody    atomic.Value // string
	lastHeaders atomic.Value // http.Header
	tsSeen      atomic.Value // []int64（order 请求 Timestamp 头序列）
}

func newD570Fixture(t *testing.T, handlers map[string]func(w http.ResponseWriter, r *http.Request)) *d570Fixture {
	f := &d570Fixture{}
	seen := []int64{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodPost && p == "/api/v4/futures/usdt/orders":
			f.orderCalls.Add(1)
			b, _ := io.ReadAll(r.Body)
			f.lastBody.Store(string(b))
			f.lastHeaders.Store(r.Header.Clone())
			if ts, _ := strconv.ParseInt(r.Header.Get("Timestamp"), 10, 64); ts > 0 {
				seen = append(seen, ts)
				f.tsSeen.Store(append([]int64(nil), seen...))
			}
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/api/v4/futures/usdt/orders/"):
			f.statusCalls.Add(1)
		case p == "/api/v4/futures/usdt/tickers":
			f.tickerCalls.Add(1)
		}
		if h, ok := handlers[r.Method+" "+p]; ok {
			h(w, r)
			return
		}
		if h, ok := handlers[p]; ok {
			h(w, r)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, p)
		http.NotFound(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	f.e = NewCEXExecutor("gate", "perp", "k", "s", "", f.srv.URL+"/api/v4", false)
	return f
}

// d570Spec 实弹形 spec JSON：leverage_max/market_order_size_max=字符串（旧码 float64 必炸=A1 对抗）。
func d570Spec(mktMax, status string) string {
	return `{"quanto_multiplier":"1","order_size_min":1,"order_size_max":1000,"enable_decimal":false,` +
		`"in_delisting":false,"leverage_max":"100","market_order_size_max":` + mktMax + `,"status":` + status + `}`
}

func d570Contracts(h map[string]func(http.ResponseWriter, *http.Request), specJSON string) {
	h["/api/v4/futures/usdt/contracts/BTC_USDT"] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, specJSON)
	}
	if _, ok := h["/api/v4/futures/usdt/accounts"]; !ok { // 预注（dual 夹具）不覆盖
		h["/api/v4/futures/usdt/accounts"] = func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"position_mode":"single"}`)
		}
	}
}

// A1：market_order_size_max="200"（字符串）+order_size_max=1000 → size=300 拒（含三值）；
// market 缺省时 order_size_max 卡法不变；leverage_max="100" 字符串解析成功值正确。
func TestD570_A1_MktSizeMaxGate(t *testing.T) {
	var specBody atomic.Value
	specBody.Store(d570Spec(`"200"`, `"trading"`))
	h := map[string]func(http.ResponseWriter, *http.Request){
		"/api/v4/futures/usdt/contracts/BTC_USDT": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, specBody.Load().(string))
		},
	}
	f := newD570Fixture(t, h)
	ctx := context.Background()
	spec, err := f.e.gateSpecFetch(ctx, "BTC_USDT")
	if err != nil {
		t.Fatalf("spec fetch: %v（旧码 leverage_max float64 撞字符串必炸=对抗锚）", err)
	}
	if spec.LeverageMax != 100 || spec.MarketOrderSizeMax != 200 {
		t.Fatalf("字符串字段解析错: %+v", spec)
	}
	// effMax=200（两正取小）→ size=300 拒，错误带三值。
	f.e.gateSpec = nil
	_, _, err = f.e.gateSpecCheck(ctx, "BTC_USDT", 300)
	if err == nil || !strings.Contains(err.Error(), "200") || !strings.Contains(err.Error(), "1000") || !strings.Contains(err.Error(), "effMax") {
		t.Fatalf("size=300 超 effMax=200 应拒且含三值: %v", err)
	}
	// market 缺省（"0"/缺席）→ order_size_max=1000 独担：size=300 放行。
	specBody.Store(d570Spec(`"0"`, `"trading"`))
	f.e.gateSpec = map[string]gateContractSpec{} // 清缓存重取
	if _, _, err = f.e.gateSpecCheck(ctx, "BTC_USDT", 300); err != nil {
		t.Fatalf("market 缺省时 order_size_max 卡法应不变: %v", err)
	}
}

// A2：status 闸——circuit_breaking 拒（含 status 值）；trading 放行；in_delisting 双保险在。
func TestD570_A2_StatusGate(t *testing.T) {
	var specBody atomic.Value
	specBody.Store(d570Spec(`"200"`, `"circuit_breaking"`))
	h := map[string]func(http.ResponseWriter, *http.Request){
		"/api/v4/futures/usdt/contracts/BTC_USDT": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, specBody.Load().(string))
		},
	}
	f := newD570Fixture(t, h)
	ctx := context.Background()
	if _, _, err := f.e.gateSpecCheck(ctx, "BTC_USDT", 100); err == nil || !strings.Contains(err.Error(), "circuit_breaking") {
		t.Fatalf("status=circuit_breaking 应拒且含 status 值: %v", err)
	}
	specBody.Store(d570Spec(`"200"`, `"trading"`))
	f.e.gateSpec = map[string]gateContractSpec{}
	if _, _, err := f.e.gateSpecCheck(ctx, "BTC_USDT", 100); err != nil {
		t.Fatalf("status=trading 应放行: %v", err)
	}
	// 双保险：status=trading + in_delisting=true 仍拒。
	specBody.Store(strings.Replace(d570Spec(`"200"`, `"trading"`), `"in_delisting":false`, `"in_delisting":true`, 1))
	f.e.gateSpec = map[string]gateContractSpec{}
	if _, _, err := f.e.gateSpecCheck(ctx, "BTC_USDT", 100); err == nil {
		t.Fatalf("in_delisting 双保险应仍拒")
	}
}

// A3：下单 POST 带 X-Gate-Exptime≈now+15s 毫秒头；GET 无此头。
func TestD570_A3_ExptimeHeader(t *testing.T) {
	var getHdr atomic.Value // http.Header（GET 请求实录）
	h := map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v4/futures/usdt/orders": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"id":1,"status":"finished","finish_as":"filled","size":1,"left":0,"fill_price":"100"}`)
		},
		"GET /api/v4/futures/usdt/orders/9": func(w http.ResponseWriter, r *http.Request) {
			getHdr.Store(r.Header.Clone())
			_, _ = io.WriteString(w, `{"id":9,"status":"finished","finish_as":"filled","size":1,"left":0,"fill_price":"100"}`)
		},
	}
	d570Contracts(h, d570Spec(`"200"`, `"trading"`))
	f := newD570Fixture(t, h)
	if _, err := f.e.placeGate(context.Background(), Order{Symbol: "BTC_USDT", Side: "BUY", Qty: 1}); err != nil {
		t.Fatalf("placeGate: %v", err)
	}
	hd := f.lastHeaders.Load().(http.Header)
	exp := hd.Get("X-Gate-Exptime")
	if exp == "" {
		t.Fatalf("下单 POST 缺 X-Gate-Exptime 头")
	}
	ms, _ := strconv.ParseInt(exp, 10, 64)
	want := time.Now().Add(15 * time.Second).UnixMilli()
	if d := ms - want; d < -2000 || d > 2000 {
		t.Fatalf("exptime=%d 应≈now+15s（diff=%dms）", ms, d)
	}
	// GET 路径无此头（gateOrderStatus 走签名 GET，不经过 exptime 逻辑）。
	if _, err := f.e.gateOrderStatus(context.Background(), "9"); err != nil {
		t.Fatalf("gateOrderStatus: %v", err)
	}
	gh := getHdr.Load().(http.Header)
	if gh.Get("X-Gate-Exptime") != "" {
		t.Fatalf("GET 请求禁带 X-Gate-Exptime")
	}
}

// A4：REQUEST_EXPIRED → gateSyncClock（X-In-Time=local+90s 微秒头）→ 同 body 重签重发；
// 第二请求 Timestamp 偏移≈90s；恒 EXPIRED → 重发=1 次上抛。
func TestD570_A4_RequestExpiredResign(t *testing.T) {
	var calls atomic.Int32
	h := map[string]func(http.ResponseWriter, *http.Request){
		"/api/v4/futures/usdt/tickers": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-In-Time", strconv.FormatInt((time.Now().Unix()+90)*1e6, 10))
			_, _ = io.WriteString(w, `[]`)
		},
		"POST /api/v4/futures/usdt/orders": func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(401)
				_, _ = io.WriteString(w, `{"label":"REQUEST_EXPIRED","message":"expired"}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":1,"status":"finished","finish_as":"filled","size":1,"left":0,"fill_price":"100"}`)
		},
	}
	d570Contracts(h, d570Spec(`"200"`, `"trading"`))
	f := newD570Fixture(t, h)
	if _, err := f.e.placeGate(context.Background(), Order{Symbol: "BTC_USDT", Side: "BUY", Qty: 1}); err != nil {
		t.Fatalf("REQUEST_EXPIRED 校时重发应成功: %v", err)
	}
	if f.tickerCalls.Load() != 1 {
		t.Fatalf("校时 tickers 应恰一次, got %d", f.tickerCalls.Load())
	}
	seen := f.tsSeen.Load().([]int64)
	if len(seen) != 2 {
		t.Fatalf("POST 应发两次, got %d", len(seen))
	}
	if d := seen[1] - seen[0]; d < 80 || d > 100 {
		t.Fatalf("重发 ts 偏移应≈90s, got %d", d)
	}
	// A4-R2 F-1：重发后 exptime 随校时 delta 偏移（删 delta 应用必红）——
	// 第二次 POST 的 X-Gate-Exptime 应≈本地钟+90s+15s（≈now+105s）。
	hd := f.lastHeaders.Load().(http.Header)
	exp, _ := strconv.ParseInt(hd.Get("X-Gate-Exptime"), 10, 64)
	if want := time.Now().UnixMilli() + 105000; exp < want-2000 || exp > want+2000 {
		t.Fatalf("重发 exptime 应随 delta 偏移≈now+105s: got diff=%dms", exp-want)
	}
	// 恒 REQUEST_EXPIRED：重发一次后上抛。
	var calls2 atomic.Int32
	h2 := map[string]func(http.ResponseWriter, *http.Request){
		"/api/v4/futures/usdt/tickers": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-In-Time", strconv.FormatInt(time.Now().Unix()*1e6, 10))
			_, _ = io.WriteString(w, `[]`)
		},
		"POST /api/v4/futures/usdt/orders": func(w http.ResponseWriter, r *http.Request) {
			calls2.Add(1)
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"label":"REQUEST_EXPIRED","message":"expired"}`)
		},
	}
	d570Contracts(h2, d570Spec(`"200"`, `"trading"`))
	f2 := newD570Fixture(t, h2)
	gateClockOffset.Store(0)
	if _, err := f2.e.placeGate(context.Background(), Order{Symbol: "BTC_USDT", Side: "BUY", Qty: 1}); err == nil {
		t.Fatalf("恒 REQUEST_EXPIRED 应上抛")
	}
	if calls2.Load() != 2 {
		t.Fatalf("重发至多一次: got %d", calls2.Load())
	}
	gateClockOffset.Store(0)
}

// A5：GET 退避——TOO_MANY_REQUESTS×2 → 第三连成功（计数=3）；429 同退避；终态即时上抛（=1）。
func TestD570_A5_GetBackoff(t *testing.T) {
	var calls atomic.Int32
	h := map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v4/futures/usdt/orders/9": func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) <= 2 {
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"label":"TOO_MANY_REQUESTS","message":"slow down"}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":9,"status":"finished","finish_as":"filled","size":1,"left":0,"fill_price":"100"}`)
		},
	}
	f := newD570Fixture(t, h)
	ord, err := f.e.gateOrderStatus(context.Background(), "9")
	if err != nil || ord.Status != "finished" {
		t.Fatalf("退避后应成功: %+v %v", ord, err)
	}
	if calls.Load() != 3 {
		t.Fatalf("限频×2 后第三连成功, calls=%d want 3", calls.Load())
	}
	// HTTP 429（无 label）同退避。
	var c2 atomic.Int32
	h["GET /api/v4/futures/usdt/orders/9"] = func(w http.ResponseWriter, r *http.Request) {
		if c2.Add(1) <= 1 {
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"message":"rate limited"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":9,"status":"finished","finish_as":"filled","size":1,"left":0,"fill_price":"100"}`)
	}
	if _, err := f.e.gateOrderStatus(context.Background(), "9"); err != nil {
		t.Fatalf("429 退避后应成功: %v", err)
	}
	if c2.Load() != 2 {
		t.Fatalf("429 退避, calls=%d", c2.Load())
	}
	// 终态 label 即时上抛（计数=1）。
	var c3 atomic.Int32
	h["GET /api/v4/futures/usdt/orders/9"] = func(w http.ResponseWriter, r *http.Request) {
		c3.Add(1)
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"label":"INVALID_PRECISION","message":"bad"}`)
	}
	if _, err := f.e.gateOrderStatus(context.Background(), "9"); err == nil {
		t.Fatalf("终态 label 应上抛")
	}
	if c3.Load() != 1 {
		t.Fatalf("终态 label 禁退避, calls=%d", c3.Load())
	}
}

// A6：dual close body 含 reduce_only:true+size:0+auto_size（三设齐）；
// single 分支不变（reduce_only:true 平仓/false 开仓）。
func TestD570_A6_DualReduceOnly(t *testing.T) {
	h := map[string]func(http.ResponseWriter, *http.Request){
		"/api/v4/futures/usdt/accounts": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"position_mode":"dual"}`)
		},
		"POST /api/v4/futures/usdt/orders": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"id":1,"status":"finished","finish_as":"filled","size":0,"left":0,"fill_price":"100"}`)
		},
	}
	d570Contracts(h, d570Spec(`"200"`, `"trading"`))
	f := newD570Fixture(t, h)
	if _, err := f.e.placeGate(context.Background(), Order{Symbol: "BTC_USDT", Side: "SELL", Qty: 1, PositionSide: "long"}); err != nil {
		t.Fatalf("dual close: %v", err)
	}
	body := f.lastBody.Load().(string)
	for _, key := range []string{`"size":0`, `"reduce_only":true`, `"auto_size":"close_long"`} {
		if !strings.Contains(body, key) {
			t.Fatalf("dual close body 缺 %s: %s", key, body)
		}
	}
}
