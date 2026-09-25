// d421_fill_confirm_test.go —— D-421：CEX 成交确认三态（受理未确认→ErrFillUnconfirmed→UNKNOWN）。
package exec

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// T1 okx 轮询窗尽 + cancel 也失败 → ErrFillUnconfirmed 携带 OrderID（旧码返 Fill{Qty:0} 无错误=红）。
func TestD421OKXUnconfirmedReturnsSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v5/public/instruments":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ctVal":"1","lotSz":"0.0001","minSz":"0.0001"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/order":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"O421","sCode":"0","accFillSz":"","avgPx":""}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/trade/order":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"state":"live","accFillSz":"","avgPx":""}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/cancel-order":
			_, _ = io.WriteString(w, `{"code":"1","msg":"cancel failed","data":[{"sCode":"1","sMsg":"network"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	e := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, false)
	fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "ETH-USDT", Side: "buy", Qty: 0.01})
	var unc *ErrFillUnconfirmed
	if !errors.As(err, &unc) {
		t.Fatalf("窗尽+cancel 失败应返 ErrFillUnconfirmed, got fill=%+v err=%v", fill, err)
	}
	if unc.OrderID != "O421" {
		t.Fatalf("ErrFillUnconfirmed.OrderID = %q, want O421", unc.OrderID)
	}
}

// T2 okx 查单网络错 → 窗内继续轮询（旧码直接 return err=红）。
func TestD421OKXPollRetriesOnNetErr(t *testing.T) {
	var gets int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v5/public/instruments":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ctVal":"1","lotSz":"0.0001","minSz":"0.0001"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/order":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"O422","sCode":"0","accFillSz":"","avgPx":""}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/trade/order":
			if atomic.AddInt64(&gets, 1) == 1 {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `bad gateway`)
				return
			}
			_, _ = io.WriteString(w, `{"code":"0","data":[{"state":"filled","accFillSz":"0.5","avgPx":"3000"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	e := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, false)
	fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "ETH-USDT", Side: "buy", Qty: 0.01})
	if err != nil {
		t.Fatalf("窗内网络错应重试而非返 err, got %v", err)
	}
	if fill.Qty != 0.5 || fill.Price != 3000 || fill.OrderID != "O422" {
		t.Fatalf("fill = %+v, want qty=0.5 price=3000 ordId=O422", fill)
	}
}

// T1b okx 窗尽 → cancel 报错（已成交）→ 终查拿到成交 → 如实 FILLED。
func TestD421OKXCancelRevealsFilled(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v5/public/instruments":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ctVal":"1","lotSz":"0.0001","minSz":"0.0001"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/order":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"O423","sCode":"0","accFillSz":"","avgPx":""}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/trade/order":
			if atomic.AddInt64(&calls, 1) <= 1 {
				_, _ = io.WriteString(w, `{"code":"0","data":[{"state":"live","accFillSz":"","avgPx":""}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"code":"0","data":[{"state":"filled","accFillSz":"0.5","avgPx":"3000","fee":"-0.1","feeCcy":"USDT"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/cancel-order":
			_, _ = io.WriteString(w, `{"code":"1","msg":"Order already filled","data":[{"sCode":"51400","sMsg":"filled"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	e := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, false)
	fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "ETH-USDT", Side: "buy", Qty: 0.01})
	if err != nil {
		t.Fatalf("cancel 揭示已成交应返 FILLED, got err=%v", err)
	}
	if fill.Qty != 0.5 || fill.Price != 3000 {
		t.Fatalf("fill = %+v", fill)
	}
}

// T1c okx 窗尽 → cancel 成功 → 终查 accFillSz=0 → 确定未成交（err 如实，非伪 FILLED）。
func TestD421OKXCancelConfirmsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v5/public/instruments":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ctVal":"1","lotSz":"0.0001","minSz":"0.0001"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/order":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"O424","sCode":"0","accFillSz":"","avgPx":""}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/trade/order":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"state":"canceled","accFillSz":"0","avgPx":""}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/cancel-order":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ordId":"O424","sCode":"0"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	e := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, false)
	_, err := e.PlaceOrder(context.Background(), Order{Symbol: "ETH-USDT", Side: "buy", Qty: 0.01})
	var unc *ErrFillUnconfirmed
	if errors.As(err, &unc) {
		t.Fatalf("确定未成交不应是 ErrFillUnconfirmed")
	}
	if err == nil {
		t.Fatalf("canceled+零成交应返 err（FAILED 映射），got nil")
	}
}

// T3 binance PARTIALLY_FILLED 不提前返回——继续轮询至 FILLED（旧码首个 partial 即返=红）。
func TestD421BinancePartialKeepsPolling(t *testing.T) {
	var gets int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v5/public/instruments":
			_, _ = io.WriteString(w, `{"code":"0","data":[{"ctVal":"1","lotSz":"0.0001","minSz":"0.0001"}]}`)
		case r.URL.Path == "/v1/positionSide/dual":
			_, _ = io.WriteString(w, `{"dualSidePosition":"true"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/order":
			_, _ = io.WriteString(w, `{"orderId":777,"executedQty":"0"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/order":
			if atomic.AddInt64(&gets, 1) <= 2 {
				_, _ = io.WriteString(w, `{"status":"PARTIALLY_FILLED","executedQty":"0.5","avgPrice":"100"}`)
				return
			}
			_, _ = io.WriteString(w, `{"status":"FILLED","executedQty":"1.0","avgPrice":"100"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/userTrades":
			_, _ = io.WriteString(w, `[]`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	e := NewCEXExecutor("binance", "perp", "k", "s", "", srv.URL, false)
	fill, err := e.PlaceOrder(context.Background(), Order{Symbol: "BTCUSDT", Side: "BUY", Qty: 1.0})
	if err != nil {
		t.Fatalf("PARTIALLY→FILLED 应正常返回, got %v", err)
	}
	if fill.Qty != 1.0 {
		t.Fatalf("PARTIALLY_FILLED 提前返回会冻结 qty=0.5，got qty=%v（应继续轮询至 1.0）", fill.Qty)
	}
	if atomic.LoadInt64(&gets) < 3 {
		t.Fatalf("应轮询至 FILLED（gets=%d < 3）", gets)
	}
}

// T4 executeLeg 遇 ErrFillUnconfirmed → LegResult UNKNOWN + OrderID（消解凭据）。
type d421UncExec struct{ venue string }

func (e *d421UncExec) Key() string { return e.venue }
func (e *d421UncExec) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	return Fill{}, &ErrFillUnconfirmed{OrderID: "ord-421"}
}
func (e *d421UncExec) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return Fill{}, errors.New("n/a")
}

func TestD421ExecuteLegUnconfirmedToUnknown(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&d421UncExec{venue: "okx:spot"})
	p := &Pipeline{registry: reg}
	res := p.executeLeg(context.Background(), LegSpec{
		VenueKey: "okx:spot", VenueSymbol: "ETH-USDT", Side: "buy", Qty: 0.01,
	}, "coid-421")
	if res.Status != "UNKNOWN" {
		t.Fatalf("ErrFillUnconfirmed 应映射 UNKNOWN, got %s", res.Status)
	}
	if res.OrderID != "ord-421" {
		t.Fatalf("UNKNOWN 腿必须携带 OrderID（消解凭据）, got %q", res.OrderID)
	}
}
