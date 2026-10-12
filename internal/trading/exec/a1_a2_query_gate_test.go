package exec

// d570b_gate_test.go —— D-570 包B 验收锚 A1-A5/A7（先红后绿+变异对抗）。
// 复用 d570Fixture（base=srv.URL+"/api/v4"）。
import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A1+A2：QueryOrderStatus gate 分支——finished+filled → filled+qty=|size-left|×quanto+px；
// orderID="" → 路径带 clOrdID；state 归一四值映射。
func TestD570B_A1A2_QueryGate(t *testing.T) {
	var lastPath atomic.Value // string
	h := map[string]func(http.ResponseWriter, *http.Request){}
	mux := map[string]string{
		"/api/v4/futures/usdt/orders/88":   `{"id":88,"status":"finished","finish_as":"filled","size":10,"left":0,"fill_price":"100"}`,
		"/api/v4/futures/usdt/orders/t-c1": `{"id":89,"status":"finished","finish_as":"cancelled","size":10,"left":10,"fill_price":"0"}`,
		"/api/v4/futures/usdt/orders/p1":   `{"id":90,"status":"open","size":10,"left":4,"fill_price":"100"}`,
		"/api/v4/futures/usdt/orders/l1":   `{"id":91,"status":"open","size":10,"left":10,"fill_price":"0"}`,
	}
	for p, body := range mux {
		body := body
		h[p] = func(w http.ResponseWriter, r *http.Request) {
			lastPath.Store(p)
			_, _ = io.WriteString(w, body)
		}
	}
	d570Contracts(h, d570Spec(`"200"`, `"trading"`))
	f := newD570Fixture(t, h)
	ctx := context.Background()
	// finished+filled：qty=|10-0|×quanto(1)=10，px=100，state=filled。
	st, qty, px, fee, err := f.e.QueryOrderStatus(ctx, "88", "", "BTC_USDT")
	if err != nil || st != "filled" || qty != 10 || px != 100 || fee != 0 {
		t.Fatalf("A1: st=%s qty=%v px=%v fee=%v err=%v", st, qty, px, fee, err)
	}
	// clOrdID 退位：orderID="" → 路径带 clOrdID。
	if _, _, _, _, err := f.e.QueryOrderStatus(ctx, "", "t-c1", "BTC_USDT"); err != nil {
		t.Fatalf("clOrdID 查单: %v", err)
	}
	if lastPath.Load().(string) != "/api/v4/futures/usdt/orders/t-c1" {
		t.Fatalf("orderID 空应退 clOrdID 路径: %v", lastPath.Load())
	}
	// A2 四值：finished+cancelled→canceled；open+部分→partial；open→live。
	if st, _, _, _, _ := f.e.QueryOrderStatus(ctx, "t-c1", "", "BTC_USDT"); st != "canceled" {
		t.Fatalf("finished+cancelled want canceled, got %s", st)
	}
	if st, q, _, _, _ := f.e.QueryOrderStatus(ctx, "p1", "", "BTC_USDT"); st != "partial" || q != 6 {
		t.Fatalf("open+部分 want partial qty=6, got %s %v", st, q)
	}
	if st, _, _, _, _ := f.e.QueryOrderStatus(ctx, "l1", "", "BTC_USDT"); st != "live" {
		t.Fatalf("open want live, got %s", st)
	}
}

// A3：gateConfirmFill 窗——前 2 拍 open、第 3 拍 finished+filled → Fill（REST=3 计数）。
func TestD570B_A3_ConfirmWindow(t *testing.T) {
	var calls atomic.Int32
	h := map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v4/futures/usdt/orders/7": func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) <= 2 {
				_, _ = io.WriteString(w, `{"id":7,"status":"open","size":10,"left":10,"fill_price":"0"}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":7,"status":"finished","finish_as":"filled","size":10,"left":0,"fill_price":"100"}`)
		},
	}
	d570Contracts(h, d570Spec(`"200"`, `"trading"`))
	f := newD570Fixture(t, h)
	fill, err := f.e.gateConfirmFill(context.Background(), Order{Symbol: "BTC_USDT"}, "7")
	if err != nil || fill.Qty != 10 || fill.Price != 100 {
		t.Fatalf("confirm fill=%+v err=%v", fill, err)
	}
	if calls.Load() != 3 {
		t.Fatalf("REST 应恰 3 拍, got %d", calls.Load())
	}
}

// A4：恒 open → 窗尽 DELETE 恰一次 + ErrFillUnconfirmed typed。
func TestD570B_A4_WindowCancel(t *testing.T) {
	var delCalls, getCalls atomic.Int32
	h := map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v4/futures/usdt/orders/7": func(w http.ResponseWriter, r *http.Request) {
			getCalls.Add(1)
			_, _ = io.WriteString(w, `{"id":7,"status":"open","size":10,"left":10,"fill_price":"0"}`)
		},
		"DELETE /api/v4/futures/usdt/orders/7": func(w http.ResponseWriter, r *http.Request) {
			delCalls.Add(1)
			_, _ = io.WriteString(w, `{"id":7,"status":"open","size":10,"left":10}`)
		},
	}
	d570Contracts(h, d570Spec(`"200"`, `"trading"`))
	f := newD570Fixture(t, h)
	start := time.Now()
	_, err := f.e.gateConfirmFill(context.Background(), Order{Symbol: "BTC_USDT"}, "7")
	var unc *ErrFillUnconfirmed
	if !errors.As(err, &unc) || unc.OrderID != "7" {
		t.Fatalf("恒 open 窗尽应 ErrFillUnconfirmed: %v", err)
	}
	if d := time.Since(start); d > fillConfirmMaxWait+2*time.Second {
		t.Fatalf("总窗应≤3s+误差: %v", d)
	}
	if delCalls.Load() != 1 {
		t.Fatalf("窗尽 DELETE 应恰一次, got %d（删撤单→本锚必红）", delCalls.Load())
	}
}

// A5：PeekOrder 桥接——lookup 终态命中零 REST 即 Fill 含 FeeUSD；canceled+0 诚实错误。
func TestD570B_A5_PeekBridge(t *testing.T) {
	var restCalls atomic.Int32
	h := map[string]func(http.ResponseWriter, *http.Request){
		"GET /api/v4/futures/usdt/orders/7": func(w http.ResponseWriter, r *http.Request) {
			restCalls.Add(1)
			_, _ = io.WriteString(w, `{"id":7,"status":"finished","finish_as":"filled","size":10,"left":0,"fill_price":"100"}`)
		},
	}
	d570Contracts(h, d570Spec(`"200"`, `"trading"`))
	f := newD570Fixture(t, h)
	f.e.SetGateOrderLookup(func(ordID string) (GateOrderSnap, bool) {
		return GateOrderSnap{State: "filled", AccFillQty: 10, AvgPx: 99, FeeUSD: 0.0009}, true
	})
	fill, err := f.e.gateConfirmFill(context.Background(), Order{Symbol: "BTC_USDT"}, "7")
	if err != nil || fill.Qty != 10 || fill.Price != 99 || fill.FeeUSD != 0.0009 {
		t.Fatalf("event fill=%+v err=%v（费=帧回填）", fill, err)
	}
	if restCalls.Load() != 0 {
		t.Fatalf("终态事件命中禁 REST（删 lookup 优先查→本锚必红）: %d", restCalls.Load())
	}
	// canceled+0 → 诚实错误。
	f.e.SetGateOrderLookup(func(string) (GateOrderSnap, bool) {
		return GateOrderSnap{State: "canceled", AccFillQty: 0}, true
	})
	if _, err := f.e.gateConfirmFill(context.Background(), Order{Symbol: "BTC_USDT"}, "7"); err == nil {
		t.Fatalf("canceled+0 须诚实错误非假成交")
	}
}

// A7：text 单号——ClientOrderID → "t-<id>"；空 → "t-nox"；>28B 截断。
func TestD570B_A7_TextTag(t *testing.T) {
	h := map[string]func(http.ResponseWriter, *http.Request){
		"POST /api/v4/futures/usdt/orders": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"id":1,"status":"finished","finish_as":"filled","size":1,"left":0,"fill_price":"100"}`)
		},
	}
	d570Contracts(h, d570Spec(`"200"`, `"trading"`))
	f := newD570Fixture(t, h)
	if _, err := f.e.placeGate(context.Background(), Order{Symbol: "BTC_USDT", Side: "BUY", Qty: 1, ClientOrderID: "abc123"}); err != nil {
		t.Fatalf("placeGate: %v", err)
	}
	if b := f.lastBody.Load().(string); !strings.Contains(b, `"text":"t-abc123"`) {
		t.Fatalf("text 单号应真实化: %s（改回常量→本锚必红）", b)
	}
	// 空 → t-arb 兜底。
	if _, err := f.e.placeGate(context.Background(), Order{Symbol: "BTC_USDT", Side: "BUY", Qty: 1}); err != nil {
		t.Fatalf("placeGate2: %v", err)
	}
	if b := f.lastBody.Load().(string); !strings.Contains(b, `"text":"t-nox"`) {
		t.Fatalf("空 ClientOrderID 应 t-arb 兜底: %s", b)
	}
	// >28 字节截断（"t-"+26 字符=28B 截）。
	if got := gateOrderText(Order{ClientOrderID: strings.Repeat("x", 40)}); len(got) != 28 {
		t.Fatalf("text 应截 28 字节, got %d (%q)", len(got), got)
	}
}
