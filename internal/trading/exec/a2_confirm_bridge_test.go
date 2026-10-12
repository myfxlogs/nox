package exec

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 验收锚 A2/A4/A5/A6（A1/A3/A7/A8 在 collect/exchange 包）。

// A2：确认桥接——终态事件命中零 REST 即返；canceled+qty>0 部分 Fill；qty=0 诚实错误；
// miss/live → REST 拍照兜底。
func TestD566B_A2_ConfirmBridge(t *testing.T) {
	ctx := context.Background()
	o := Order{Symbol: "BTC-USDT"}

	// filled 终态 → 零 REST（server 任何请求=fail）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected REST %s %s（终态事件命中应零 REST）", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	e := NewCEXExecutor("okx", "perp", "k", "s", "p", srv.URL, true)
	e.SetOKXOrderLookup(func(ordID string) (OKXOrderSnap, bool) {
		return OKXOrderSnap{State: "filled", AccFillQty: 0.5, AvgPx: 60000, FeeUSD: 1.2}, true
	})
	f, err := e.okxConfirmFill(ctx, o, "BTC-USDT-SWAP", "ord1", 0.01)
	if err != nil || f.Qty != 0.5 || f.Price != 60000 || f.FeeUSD != 1.2 {
		t.Fatalf("fill=%+v err=%v want qty=0.5 px=60000 fee=1.2", f, err)
	}

	// canceled+qty>0 → 部分 Fill。
	e.SetOKXOrderLookup(func(string) (OKXOrderSnap, bool) {
		return OKXOrderSnap{State: "canceled", AccFillQty: 0.2, AvgPx: 59000, FeeUSD: 0.5}, true
	})
	f, err = e.okxConfirmFill(ctx, o, "BTC-USDT-SWAP", "ord2", 0.01)
	if err != nil || f.Qty != 0.2 {
		t.Fatalf("partial fill=%+v err=%v want qty=0.2", f, err)
	}

	// canceled+qty=0 → 诚实错误。
	var alerts int
	e.AlertFn = func(posID *int64, level, msg string) { alerts++ }
	o.PosID = 7 // alertCritical PosID>0 才发
	e.SetOKXOrderLookup(func(string) (OKXOrderSnap, bool) {
		return OKXOrderSnap{State: "canceled"}, true
	})
	if _, err = e.okxConfirmFill(ctx, o, "BTC-USDT-SWAP", "ord3", 0.01); err == nil {
		t.Fatal("canceled qty=0 应诚实错误")
	}
	if alerts != 1 {
		t.Fatalf("alerts=%d want 1", alerts)
	}

	// live（非终态）→ 落 REST 拍照兜底。
	var restCalls int32
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v5/trade/order" {
			atomic.AddInt32(&restCalls, 1)
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"ord4","state":"filled","accFillSz":"0.3","avgPx":"60000","fee":"","feeCcy":""}]}`)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv2.Close()
	e2 := NewCEXExecutor("okx", "perp", "k", "s", "p", srv2.URL, true)
	e2.SetOKXOrderLookup(func(string) (OKXOrderSnap, bool) {
		return OKXOrderSnap{State: "live"}, true // 永非终态
	})
	f, err = e2.okxConfirmFill(ctx, o, "BTC-USDT-SWAP", "ord4", 1)
	if err != nil || f.Qty != 0.3 {
		t.Fatalf("REST fill=%+v err=%v want qty=0.3", f, err)
	}
	if atomic.LoadInt32(&restCalls) < 1 {
		t.Fatal("live 事件应落 REST 兜底")
	}
}

// A4：singleflight——20 并发 okxPosModeCheck，fake server 带延迟强制重叠 → GET 计数=1。
func TestD566B_A4_PosModeSingleflight(t *testing.T) {
	var gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/account/config" {
			atomic.AddInt32(&gets, 1)
			time.Sleep(80 * time.Millisecond) // 强制在途重叠
			_, _ = io.WriteString(w, `{"code":"0","data":[{"posMode":"long_short_mode"}]}`)
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	e := NewCEXExecutor("okx", "perp", "k", "s", "p", srv.URL, true)

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- e.okxPosModeCheck(context.Background())
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("posModeCheck: %v", err)
		}
	}
	if got := atomic.LoadInt32(&gets); got != 1 {
		t.Fatalf("GET account/config=%d want 1（singleflight 在途合并）", got)
	}
}

// A5：TTL 判龄 + Invalidate——at 拨回 25h → 重取；OKXSpecInvalidate → 重取。
func TestD566B_A5_SpecTTLAndInvalidate(t *testing.T) {
	instID := "D566B-USDT-SWAP"
	var gets int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/public/instruments" {
			atomic.AddInt32(&gets, 1)
			_, _ = io.WriteString(w, fmt.Sprintf(d566InstrumentsPerp, "100", instID))
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	ctx := context.Background()

	if _, _, _, err := OkxSwapSpec(ctx, srv.Client(), srv.URL, instID); err != nil {
		t.Fatalf("prime: %v", err)
	}
	if _, _, _, err := OkxSwapSpec(ctx, srv.Client(), srv.URL, instID); err != nil {
		t.Fatalf("cached: %v", err)
	}
	if got := atomic.LoadInt32(&gets); got != 1 {
		t.Fatalf("prime gets=%d want 1（缓存命中）", got)
	}

	// at 拨回 25h → 超龄重取。
	okxSwapSpecCache.Store(instID, okxSwapSpec{ctVal: 1, lotSz: 1, maxMktSz: 100, at: time.Now().Add(-25 * time.Hour)})
	if _, _, _, err := OkxSwapSpec(ctx, srv.Client(), srv.URL, instID); err != nil {
		t.Fatalf("ttl refetch: %v", err)
	}
	if got := atomic.LoadInt32(&gets); got != 2 {
		t.Fatalf("ttl gets=%d want 2（25h 超龄重取）", got)
	}

	// Invalidate → 重取。
	OKXSpecInvalidate(instID)
	if _, _, _, err := OkxSwapSpec(ctx, srv.Client(), srv.URL, instID); err != nil {
		t.Fatalf("invalidate refetch: %v", err)
	}
	if got := atomic.LoadInt32(&gets); got != 3 {
		t.Fatalf("invalidate gets=%d want 3", got)
	}
}

// A6：拒单驱动失效——sCode=51121 拒单 → OKXSpecInvalidate → 下一单 spec 重取。
func TestD566B_A6_RejectInvalidate(t *testing.T) {
	instID := "D566C-USDT-SWAP"
	var specGets, posts int32
	var reject atomic.Bool
	reject.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/public/instruments":
			atomic.AddInt32(&specGets, 1)
			_, _ = io.WriteString(w, fmt.Sprintf(d566InstrumentsPerp, "100", instID))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/account/config":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"posMode":"long_short_mode"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/order":
			atomic.AddInt32(&posts, 1)
			if reject.Load() {
				_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"x","sCode":"51121","sMsg":"below min"}]}`)
			} else {
				_, _ = io.WriteString(w, `{"code":"0","msg":"","data":[{"ordId":"x","sCode":"0","accFillSz":"1","avgPx":"100"}]}`)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/trade/order":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"x","state":"filled","accFillSz":"1","avgPx":"100","fee":"","feeCcy":""}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	e := NewCEXExecutor("okx", "perp", "k", "s", "p", srv.URL, true)
	e.AlertFn = func(*int64, string, string) {}
	o := Order{Symbol: strings.TrimSuffix(instID, "-SWAP"), Qty: 1, Side: "BUY"}

	if _, err := e.placeOKX(ctx, o); err == nil {
		t.Fatal("51121 拒单应返错")
	}
	// 首单：okxStateCheck + okxContractVal 各 GET 一次 instruments。
	if atomic.LoadInt32(&specGets) != 2 {
		t.Fatalf("specGets=%d want 2（state 闸+spec 各一）", specGets)
	}
	// 拒单已失效缓存 → 第二单 state+spec 双表重取（各 +1）。
	reject.Store(false)
	if _, err := e.placeOKX(ctx, o); err != nil {
		t.Fatalf("second order: %v", err)
	}
	if atomic.LoadInt32(&specGets) != 4 {
		t.Fatalf("specGets=%d want 4（拒单驱动失效后双表重取）", specGets)
	}
	if atomic.LoadInt32(&posts) != 2 {
		t.Fatalf("posts=%d want 2", posts)
	}
}
