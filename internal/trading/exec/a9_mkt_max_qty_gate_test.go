package exec

// d569b_binance_test.go —— D-569 包B 验收锚 A9-A12（先红后绿+变异对抗）。
// perp 夹具生产形状：base=srv.URL+"/fapi"（exchange_configs 约定），路由 /fapi/v1/*。
import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// resetPerpFilters 清 perp 过滤器缓存+dualSide 探测态（测试间隔离）。
func resetPerpFilters() {
	binanceFiltersMu.Lock()
	binanceFiltersCache = map[string]binanceFilters{}
	binanceFiltersMu.Unlock()
	resetSpotFilters()
}

// d569bPerpFixture perp 夹具：handlers 按路径注入，计数器原子。
type d569bPerpFixture struct {
	srv                                                                       *httptest.Server
	e                                                                         *CEXExecutor
	exchInfoCalls, orderCalls, dualCalls, statusCalls, tradesCalls, markCalls atomic.Int32
}

func newD569bPerpFixture(t *testing.T, handlers map[string]func(w http.ResponseWriter, r *http.Request)) *d569bPerpFixture {
	f := &d569bPerpFixture{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch p {
		case "/fapi/v1/exchangeInfo":
			f.exchInfoCalls.Add(1)
		case "/fapi/v1/order":
			if r.Method == http.MethodGet {
				f.statusCalls.Add(1)
			} else {
				f.orderCalls.Add(1)
			}
		case "/fapi/v1/positionSide/dual":
			f.dualCalls.Add(1)
		case "/fapi/v1/userTrades":
			f.tradesCalls.Add(1)
		case "/fapi/v1/premiumIndex":
			f.markCalls.Add(1)
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
	f.e = NewCEXExecutor("binance", "perp", "k", "s", "", f.srv.URL+"/fapi", false)
	return f
}

const d569bFiltersMkt200 = `{"symbols":[{"symbol":"BTCUSDT","filters":[` +
	`{"filterType":"LOT_SIZE","minQty":"0.001","maxQty":"9000","stepSize":"0.001"},` +
	`{"filterType":"MARKET_LOT_SIZE","minQty":"0","maxQty":"200","stepSize":"0"},` +
	`{"filterType":"MIN_NOTIONAL","notional":"5"}]}]}`

// A9：MARKET_LOT_SIZE.maxQty=200 → qty 300 拒；错误文案同时含市价上限与步进 floor 语义；
// floor 用 LOT_SIZE.stepSize=0.001（MARKET_LOT_SIZE.stepSize=0 实拍不采）。
func TestD569B_A9_MktMaxQtyGate(t *testing.T) {
	resetPerpFilters()
	f := newD569bPerpFixture(t, map[string]func(http.ResponseWriter, *http.Request){
		"/fapi/v1/exchangeInfo": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, d569bFiltersMkt200)
		},
	})
	_, err := f.e.binanceQtyGuard(context.Background(), "BTCUSDT", 300, 0)
	if err == nil {
		t.Fatalf("qty=300 超 MARKET_LOT_SIZE.maxQty=200 应拒")
	}
	if !strings.Contains(err.Error(), "200") || !strings.Contains(err.Error(), "MARKET_LOT_SIZE") {
		t.Fatalf("拒因须含市价上限值: %v", err)
	}
	if f.orderCalls.Load() != 0 {
		t.Fatalf("闸拒单禁发 POST, got %d", f.orderCalls.Load())
	}
	// LOT_SIZE.maxQty 闸独立：LOT=150 < MKT 上限走 LOT 拒（变异对抗——删 LOT 判定即红）。
	binanceFiltersMu.Lock()
	fc := binanceFiltersCache["BTCUSDT"]
	fc.MktMaxQty, fc.LotMaxQty = 9000, 100
	binanceFiltersCache["BTCUSDT"] = fc
	binanceFiltersMu.Unlock()
	_, err = f.e.binanceQtyGuard(context.Background(), "BTCUSDT", 300, 0)
	if err == nil || !strings.Contains(err.Error(), "LOT_SIZE") {
		t.Fatalf("LOT_SIZE.maxQty=100 应拒 qty=300: %v", err)
	}
	// floor 生效断言：qty=0.0019 → floor 0.001 < minQty 0.001? 0.001 等界放行；
	// 用 0.0014 → floor 0.001 正好过（stepSize=0.001 采 LOT_SIZE，非 MKT 的 0）。
	binanceFiltersMu.Lock()
	fc.MktMaxQty, fc.LotMaxQty = 200, 9000
	binanceFiltersCache["BTCUSDT"] = fc
	binanceFiltersMu.Unlock()
	g, err := f.e.binanceQtyGuard(context.Background(), "BTCUSDT", 0.0014, 0)
	if err != nil || g != 0.001 {
		t.Fatalf("floor 应用 LOT_SIZE.stepSize=0.001: got %v err=%v", g, err)
	}
}

// A10：确认桥——终态事件命中零 REST status 返回 Fill（费=REST userTrades 恰一次）；
// 事件 miss → REST 拍照兜底。
func TestD569B_A10_ConfirmEventBridge(t *testing.T) {
	resetPerpFilters()
	f := newD569bPerpFixture(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /fapi/v1/order": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"status":"FILLED","executedQty":"1.0","avgPrice":"100"}`)
		},
		"GET /fapi/v1/userTrades": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `[{"commission":"0.05","commissionAsset":"USDT"}]`)
		},
	})
	o := Order{Symbol: "BTCUSDT", Side: "BUY", Qty: 1}
	// 事件命中：filled+qty>0 → Fill 直返，REST status 零调用，费 REST 恰一次。
	f.e.SetBinanceOrderLookup(func(ordID string) (BinanceOrderSnap, bool) {
		if ordID != "777" {
			t.Fatalf("lookup ordID=%q want 777", ordID)
		}
		return BinanceOrderSnap{State: "filled", AccFillQty: 0.8, AvgPx: 99}, true
	})
	fill, err := f.e.binanceConfirmFill(context.Background(), o, "777")
	if err != nil {
		t.Fatalf("event confirm: %v", err)
	}
	if fill.Qty != 0.8 || fill.Price != 99 || fill.FeeUSD != 0.05 {
		t.Fatalf("fill=%+v（事件量/价+REST 费）", fill)
	}
	if f.statusCalls.Load() != 0 {
		t.Fatalf("终态事件命中禁 REST status, got %d", f.statusCalls.Load())
	}
	if f.tradesCalls.Load() != 1 {
		t.Fatalf("费 REST 恰一次, got %d", f.tradesCalls.Load())
	}
	// canceled qty=0 → 诚实错误（仍零 REST status）。
	f.e.SetBinanceOrderLookup(func(string) (BinanceOrderSnap, bool) {
		return BinanceOrderSnap{State: "canceled", AccFillQty: 0}, true
	})
	if _, err := f.e.binanceConfirmFill(context.Background(), o, "777"); err == nil {
		t.Fatalf("canceled+0 量须返诚实错误")
	}
	if f.statusCalls.Load() != 0 {
		t.Fatalf("终态事件禁 REST status, got %d", f.statusCalls.Load())
	}
	// miss → REST 兜底（status FILLED 命中）。
	f.e.SetBinanceOrderLookup(func(string) (BinanceOrderSnap, bool) { return BinanceOrderSnap{}, false })
	fill, err = f.e.binanceConfirmFill(context.Background(), o, "777")
	if err != nil || fill.Qty != 1.0 {
		t.Fatalf("miss 应 REST 兜底: fill=%+v err=%v", fill, err)
	}
	if f.statusCalls.Load() == 0 {
		t.Fatalf("miss 须落 REST status 拍照")
	}
}

// A11：-4061 → dualSide 缓存失效+重探+critical 告警；本单不重试。
func TestD569B_A11_DualSideInvalidate(t *testing.T) {
	resetPerpFilters()
	var alerts atomic.Int32
	f := newD569bPerpFixture(t, map[string]func(http.ResponseWriter, *http.Request){
		"/fapi/v1/exchangeInfo": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, d569bFiltersMkt200)
		},
		"/fapi/v1/positionSide/dual": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"dualSidePosition":true}`)
		},
		"POST /fapi/v1/order": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"code":-4061,"msg":"Order's position side does not match user's setting"}`)
		},
	})
	f.e.AlertFn = func(*int64, string, string) { alerts.Add(1) }
	ord := Order{PosID: 1, Symbol: "BTCUSDT", Side: "SELL", Qty: 0.5, PositionSide: "long"}
	if _, err := f.e.PlaceOrder(context.Background(), ord); err == nil {
		t.Fatalf("-4061 应拒单返错")
	}
	if _, err := f.e.PlaceOrder(context.Background(), ord); err == nil {
		t.Fatalf("第二单同 -4061 应拒（本单不重试）")
	}
	if got := f.dualCalls.Load(); got != 2 {
		t.Fatalf("-4061 后 dualSide 须重探: probe=%d want 2", got)
	}
	if got := f.orderCalls.Load(); got != 2 {
		t.Fatalf("每单至多一次 POST（不重试）: got %d", got)
	}
	if alerts.Load() < 2 {
		t.Fatalf("critical 告警须发（dualSide 失效+拒单两路）: got %d", alerts.Load())
	}
}

// A12：25h 陈缓存强制重取；-1013 拒单 → perp+spot 两表失效，下一单重取 exchangeInfo。
func TestD569B_A12_FiltersTTLInvalidate(t *testing.T) {
	resetPerpFilters()
	f := newD569bPerpFixture(t, map[string]func(http.ResponseWriter, *http.Request){
		"/fapi/v1/exchangeInfo": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, d569bFiltersMkt200)
		},
		"/fapi/v1/positionSide/dual": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"dualSidePosition":true}`)
		},
		"POST /fapi/v1/order": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"code":-1013,"msg":"Filter failure"}`)
		},
	})
	// 预置 25h 陈 perp 缓存 + spot 缓存 → 首单 force refetch。
	binanceFiltersMu.Lock()
	binanceFiltersCache["BTCUSDT"] = binanceFilters{StepSize: 0.001, MinQty: 0.001, at: time.Now().Add(-25 * time.Hour)}
	binanceFiltersMu.Unlock()
	spotFiltersMu.Lock()
	spotFiltersCache["BTCUSDT"] = binanceSpotFilters{StepSize: 0.001, at: time.Now()}
	spotFiltersMu.Unlock()
	ord := Order{PosID: 1, Symbol: "BTCUSDT", Side: "SELL", Qty: 0.5, PositionSide: "long"}
	if _, err := f.e.PlaceOrder(context.Background(), ord); err == nil {
		t.Fatalf("-1013 应返错")
	}
	if got := f.exchInfoCalls.Load(); got != 1 {
		t.Fatalf("25h 陈缓存须重取: exchangeInfo=%d want 1", got)
	}
	spotFiltersMu.Lock()
	_, spotStill := spotFiltersCache["BTCUSDT"]
	spotFiltersMu.Unlock()
	if spotStill {
		t.Fatalf("-1013 须同删 spot 缓存")
	}
	// 第二单：-1013 已失效 perp 缓存 → 再取。
	if _, err := f.e.PlaceOrder(context.Background(), ord); err == nil {
		t.Fatalf("第二单仍 -1013 返错（不重试本单，重取的是过滤器）")
	}
	if got := f.exchInfoCalls.Load(); got != 2 {
		t.Fatalf("失效后下一单须重取 exchangeInfo: got %d want 2", got)
	}
	// 变异对抗：新鲜缓存不重取——第三单同窗内命中缓存 exchangeInfo 不增。
	binanceFiltersMu.Lock()
	binanceFiltersCache["BTCUSDT"] = binanceFilters{StepSize: 0.001, MinQty: 0.001, MktMaxQty: 200, at: time.Now()}
	binanceFiltersMu.Unlock()
	if _, err := f.e.binanceQtyGuard(context.Background(), "BTCUSDT", 0.5, 0); err != nil {
		t.Fatalf("新鲜缓存直接命中: %v", err)
	}
	if got := f.exchInfoCalls.Load(); got != 2 {
		t.Fatalf("新鲜缓存禁重取: exchangeInfo=%d", got)
	}
}
