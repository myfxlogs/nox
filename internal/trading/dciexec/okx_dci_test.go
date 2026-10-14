// okx_dci_test.go —— okx DCI 执行径锚：quote→trade 两段式序列+报价过期如实败。
package dciexec

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// okxDCISrv dcd 段式 mock：funding 100/unified 1000→申购 qty 时短差补划→
// quote→trade→order-status 序列记 calls；expired=true 时 validUntil 过去态。
func okxDCISrv(t *testing.T, calls *[]string, expired bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v5/account/balance":
			w.Write([]byte(`{"code":"0","data":[{"details":[{"ccy":"USDT","availBal":"1000"}]}]}`))
		case "/api/v5/asset/balances":
			w.Write([]byte(`{"code":"0","data":[{"ccy":"USDT","availBal":"100"}]}`))
		case "/api/v5/asset/transfer":
			w.Write([]byte(`{"code":"0","data":[{"transId":"1"}]}`))
		case "/api/v5/finance/sfp/dcd/quote":
			vu := time.Now().Add(time.Hour).UnixMilli()
			if expired {
				vu = time.Now().Add(-time.Hour).UnixMilli()
			}
			fmt.Fprintf(w, `{"code":"0","data":[{"quoteId":"qt1","validUntil":"%d"}]}`, vu)
		case "/api/v5/finance/sfp/dcd/trade":
			w.Write([]byte(`{"code":"0","data":[{"ordId":"11401353"}]}`))
		case "/api/v5/finance/sfp/dcd/order-status":
			w.Write([]byte(`{"code":"0","data":[{"ordId":"11401353","state":"LIVE"}]}`))
		default:
			w.Write([]byte(`{"code":"50100","msg":"unknown","data":[]}`))
		}
	}))
}

func TestOkxDCILeg_QuoteTradeSequence(t *testing.T) {
	var calls []string
	srv := okxDCISrv(t, &calls, false)
	defer srv.Close()
	st := newFake()
	tenor := 7
	deliv := time.Now().AddDate(0, 0, 7)
	st.quotes = append(st.quotes, pgstore.CexDCIQuote{
		Venue: "okx", Underlying: "BTC", InvestCcy: "USDT", OptType: "PUT",
		Strike: f64(80000), TenorDays: &tenor, APY: 0.11,
		ProductRef: "BTC-USDT-260327-80000-P", DeliveryTs: &deliv})
	ex := &Executor{Store: st, Client: srv.Client(), Now: time.Now,
		EarnCreds: map[string]EarnCred{"okx": {Key: "k", Secret: "s", Pass: "p", BaseURL: srv.URL}}}
	propID := int64(9)
	msg, err := ex.tryLegOKX(context.Background(), gate.PutLeg, 1.0, 90, 100000, 90000, &propID)
	if err != nil {
		t.Fatalf("tryLegOKX: %v", err)
	}
	if !strings.Contains(msg, "入账") {
		t.Fatalf("成文 msg: %s", msg)
	}
	// 序列断言：balance→balance→transfer→quote→trade→order-status。
	want := []string{"transfer", "dcd/quote", "dcd/trade", "order-status"}
	idx := 0
	for _, c := range calls {
		if idx < len(want) && strings.Contains(c, want[idx]) {
			idx++
		}
	}
	if idx != len(want) {
		t.Fatalf("端点序列: %v", calls)
	}
	// 订单 venue=okx 落账。
	var o *pgstore.DCIOrder
	for _, v := range st.orders {
		o = v
	}
	if o == nil || o.Venue != "okx" || o.ProductRef != "BTC-USDT-260327-80000-P" {
		t.Fatalf("订单行: %+v", o)
	}
}

func TestOkxDCILeg_ExpiredQuoteFails(t *testing.T) {
	var calls []string
	srv := okxDCISrv(t, &calls, true)
	defer srv.Close()
	st := newFake()
	tenor := 7
	deliv := time.Now().AddDate(0, 0, 7)
	st.quotes = append(st.quotes, pgstore.CexDCIQuote{
		Venue: "okx", Underlying: "BTC", InvestCcy: "USDT", OptType: "PUT",
		Strike: f64(80000), TenorDays: &tenor, APY: 0.11,
		ProductRef: "BTC-USDT-260327-80000-P", DeliveryTs: &deliv})
	ex := &Executor{Store: st, Client: srv.Client(), Now: time.Now,
		EarnCreds: map[string]EarnCred{"okx": {Key: "k", Secret: "s", Pass: "p", BaseURL: srv.URL}}}
	propID := int64(9)
	if _, err := ex.tryLegOKX(context.Background(), gate.PutLeg, 1.0, 90, 100000, 90000, &propID); err == nil {
		t.Fatal("过期报价应如实败")
	}
	for _, c := range calls {
		if strings.Contains(c, "dcd/trade") {
			t.Fatalf("过期报价不应发 trade: %v", calls)
		}
	}
}

func TestOkxDCILeg_NoCredGap(t *testing.T) {
	st := newFake()
	ex := &Executor{Store: st, Now: time.Now, EarnCreds: map[string]EarnCred{}}
	propID := int64(9)
	if _, err := ex.tryLegOKX(context.Background(), gate.PutLeg, 1.0, 90, 100000, 90000, &propID); err == nil {
		t.Fatal("凭据缺应 credential 缺口")
	}
}
