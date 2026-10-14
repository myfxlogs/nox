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
		ProductRef: "BTC-USDT-260327-80000-P", DeliveryTs: &deliv,
		MinAmount: f64(10), PurchaseDecimal: idec(0)})
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
		ProductRef: "BTC-USDT-260327-80000-P", DeliveryTs: &deliv,
		MinAmount: f64(10), PurchaseDecimal: idec(0)})
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

// TestOkxDCILeg_ConstraintGate 约束闸三态：qty<minSize 如实败零 venue 打面；
// 约束行缺 fail-closed 不臆默认；达标档照常过闸（序列锚已覆）。
func TestOkxDCILeg_ConstraintGate(t *testing.T) {
	mk := func(q pgstore.CexDCIQuote, calls *[]string) (*httptest.Server, *Executor) {
		srv := okxDCISrv(t, calls, false)
		st := newFake()
		st.quotes = append(st.quotes, q)
		return srv, &Executor{Store: st, Client: srv.Client(), Now: time.Now,
			EarnCreds: map[string]EarnCred{"okx": {Key: "k", Secret: "s", Pass: "p", BaseURL: srv.URL}}}
	}
	tenor := 7
	deliv := time.Now().AddDate(0, 0, 7)
	base := func() pgstore.CexDCIQuote {
		return pgstore.CexDCIQuote{Venue: "okx", Underlying: "BTC", InvestCcy: "USDT",
			OptType: "PUT", Strike: f64(80000), TenorDays: &tenor, APY: 0.11,
			ProductRef: "P1", DeliveryTs: &deliv,
			MinAmount: f64(10), PurchaseDecimal: idec(0)}
	}
	// qty<min：余额 funding100+unified1000×share 0.005=5.5 < min10 → 拒零打面。
	var calls []string
	q := base()
	srv, ex := mk(q, &calls)
	defer srv.Close()
	propID := int64(9)
	if _, err := ex.tryLegOKX(context.Background(), gate.PutLeg, 0.005, 90, 100000, 90000, &propID); err == nil {
		t.Fatal("约束不达标应拒")
	}
	for _, c := range calls {
		if strings.Contains(c, "dcd/") || strings.Contains(c, "transfer") {
			t.Fatalf("约束不达标不应打 venue: %v", calls)
		}
	}
	// 约束行缺（MinAmount nil）→ fail-closed 拒。
	calls = nil
	q2 := base()
	q2.MinAmount, q2.PurchaseDecimal = nil, nil
	srv2, ex2 := mk(q2, &calls)
	defer srv2.Close()
	if _, err := ex2.tryLegOKX(context.Background(), gate.PutLeg, 1.0, 90, 100000, 90000, &propID); err == nil {
		t.Fatal("约束行缺应 fail-closed")
	}
	for _, c := range calls {
		if strings.Contains(c, "dcd/") {
			t.Fatalf("约束缺不应打 venue: %v", calls)
		}
	}
}

// TestOkxDCILeg_DustShortfall 尘级短差跳划转：funding 差 ≤1e-6、unified 仅尘
// →不发 asset/transfer，申购照常走 dcd 序列（earn 轨同语义照搬——旧 min() 语义
// 会发尘划转被 venue 51000 拒，SG #22/#24/#445 同型实证）。
func TestOkxDCILeg_DustShortfall(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v5/account/balance":
			w.Write([]byte(`{"code":"0","data":[{"details":[{"ccy":"USDT","availBal":"0.0000005"}]}]}`))
		case "/api/v5/asset/balances":
			w.Write([]byte(`{"code":"0","data":[{"ccy":"USDT","availBal":"99.9999995"}]}`))
		case "/api/v5/finance/sfp/dcd/quote":
			fmt.Fprintf(w, `{"code":"0","data":[{"quoteId":"qt1","validUntil":"%d"}]}`,
				time.Now().Add(time.Hour).UnixMilli())
		case "/api/v5/finance/sfp/dcd/trade":
			w.Write([]byte(`{"code":"0","data":[{"ordId":"1"}]}`))
		case "/api/v5/finance/sfp/dcd/order-status":
			w.Write([]byte(`{"code":"0","data":[{"ordId":"1","state":"LIVE"}]}`))
		default:
			w.Write([]byte(`{"code":"50100","msg":"unknown","data":[]}`))
		}
	}))
	defer srv.Close()
	st := newFake()
	tenor := 7
	deliv := time.Now().AddDate(0, 0, 7)
	st.quotes = append(st.quotes, pgstore.CexDCIQuote{
		Venue: "okx", Underlying: "BTC", InvestCcy: "USDT", OptType: "PUT",
		Strike: f64(80000), TenorDays: &tenor, APY: 0.11,
		ProductRef: "P1", DeliveryTs: &deliv,
		MinAmount: f64(10), PurchaseDecimal: idec(0)})
	ex := &Executor{Store: st, Client: srv.Client(), Now: time.Now,
		EarnCreds: map[string]EarnCred{"okx": {Key: "k", Secret: "s", Pass: "p", BaseURL: srv.URL}}}
	propID := int64(9)
	// qty=(99.9999995+5e-7)→floor=100 ≥min10；needed=5e-7 尘差→跳划转照常申购。
	if _, err := ex.tryLegOKX(context.Background(), gate.PutLeg, 1.0, 90, 100000, 90000, &propID); err != nil {
		t.Fatalf("尘差应跳划转照常申购: %v", err)
	}
	for _, c := range calls {
		if strings.Contains(c, "asset/transfer") {
			t.Fatalf("尘级短差不应发划转: %v", calls)
		}
	}
	if !strings.Contains(strings.Join(calls, ";"), "dcd/trade") {
		t.Fatalf("应走完 dcd 序列: %v", calls)
	}
}

// TestOkxSettle_SettledBooks 行权/到期对账锚：order-status=settled→CAS 翻态+
// 利息入账+warn；LIVE 未终态零翻。接货态（settleCcy≠invest）走处置钩。
func TestOkxSettle_SettledBooks(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v5/finance/sfp/dcd/order-status"):
			w.Write([]byte(`{"code":"0","data":[{"ordId":"11401353","state":"settled","settleSz":"1010.5","settleCcy":"USDT","yieldSz":"10.5","yieldCcy":"USDT","settlePx":"78000"}]}`))
		default:
			w.Write([]byte(`{"code":"50100","data":[]}`))
		}
	}))
	defer srv.Close()
	st := newFake()
	st.cfgID = 42
	ex := &Executor{Store: st, Client: srv.Client(), Now: time.Now,
		EarnCreds: map[string]EarnCred{"okx": {Key: "k", Secret: "s", Pass: "p", BaseURL: srv.URL}}}
	td := 7
	pid := int64(9)
	o := pgstore.FilledDCIOrder{ID: 7, RequestID: "dci|okx|PUT|P|x|1", Venue: "okx",
		ProductRef: "BTC-USDT-260327-80000-P", OptType: "PUT", InvestCcy: "USDT",
		Underlying: "BTC", Qty: 1000, TenorDays: &td, ProposalID: &pid,
		Response: `{"ordId":"11401353"}`}
	done, err := ex.settleOKXOrder(context.Background(), o)
	if err != nil || !done {
		t.Fatalf("settle: done=%v err=%v", done, err)
	}
	if len(st.settled) != 1 || st.settled[0].ccy != "USDT" || st.settled[0].qty != 1010.5 {
		t.Fatalf("翻态行: %+v", st.settled)
	}
	if st.settled[0].income != 10.5 {
		t.Fatalf("利息额: %+v", st.settled[0])
	}
	// LIVE 态→零翻。
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":"0","data":[{"ordId":"1","state":"live"}]}`))
	}))
	defer srv2.Close()
	ex.Client = srv2.Client()
	ex.EarnCreds["okx"] = EarnCred{Key: "k", Secret: "s", Pass: "p", BaseURL: srv2.URL}
	done2, err := ex.settleOKXOrder(context.Background(), o)
	if err != nil || done2 {
		t.Fatalf("LIVE 零翻: done=%v err=%v", done2, err)
	}
}

// TestOkxSettle_EarnRowSkipped earn 轨申购行（request_id=earn|…、回执无 ordId）
// 不进 okx order-status 轮询——否则每拍「缺 ordId」报错刷屏；dci|okx| 前缀
// 行照常轮询。
func TestOkxSettle_EarnRowSkipped(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		// sfp/dcd 族 code 实拍是数字非字符串（SG order-status 实证）。
		w.Write([]byte(`{"code":0,"data":[{"ordId":"1","state":"live"}]}`))
	}))
	defer srv.Close()
	st := newFake()
	st.filledOrders = []pgstore.FilledDCIOrder{
		{ID: 26, RequestID: "earn|USDT|p378|7", Venue: "okx", OptType: "EARN",
			Response: `[{"amt":"1","ccy":"USDT","side":"purchase"}]`},
		{ID: 30, RequestID: "dci|okx|probe|P|x|1", Venue: "okx", OptType: "PUT",
			Response: `{"ordId":"11401353"}`},
	}
	ex := &Executor{Store: st, Client: srv.Client(), Now: time.Now,
		EarnCreds: map[string]EarnCred{"okx": {Key: "k", Secret: "s", Pass: "p", BaseURL: srv.URL}}}
	if _, err := ex.SettleExpiredOrders(context.Background()); err != nil {
		t.Fatalf("settle: %v", err)
	}
	n := 0
	for _, c := range calls {
		if strings.Contains(c, "order-status") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("order-status 应只轮 dci 前缀行（earn 行滤掉）: %v", calls)
	}
}
