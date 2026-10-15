// d204_test.go —— D-204 锚：休眠轨 go 拒单带因/裸 go 跳次席/约束行解析/deferred 不重演。
package dciexec

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// 休眠轨（D-191R7）：go 休眠提案→带因拒单非市况文案。
func TestD204_DormantGoReject(t *testing.T) {
	fs := newFake()
	one := 1.0
	fs.cons = []pgstore.CapabilityConstraint{
		{Subject: "dormant:dci_sell_high_covered_call@binance", ValueNum: &one},
		{Subject: "dormant_inactive:x@y", ValueNum: f64(0)}, // 非 1 不生效
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	e := &Executor{SigmaFn: testSigma, Store: fs, BnKey: "k", BnSec: "s", Sapi: srv.URL,
		Client: srv.Client(), Now: time.Now,
		PriceFn: func(context.Context, string) (float64, error) { return 100000, nil }}
	pid := fs.addProposal("dci_sell_high_covered_call")
	_, err := e.GoProposal(context.Background(), pid)
	if err == nil || !strings.Contains(err.Error(), "休眠轨（D-191R7）dci_sell_high_covered_call@binance") {
		t.Fatalf("休眠拒单缺：%v", err)
	}
	// 未接执行轨模板休眠行不影响（模板闸先拦）。
	pid2 := fs.addProposal("other_tmpl")
	_, err = e.GoProposal(context.Background(), pid2)
	if err == nil || !strings.Contains(err.Error(), "未接执行轨") {
		t.Fatalf("模板闸应先拦：%v", err)
	}
}

// 裸 go 跳休眠提案取次席（最新=休眠 covered_call → 取 buy_low）。
func TestD204_BareGoSkipsDormant(t *testing.T) {
	fs := newFake()
	one := 1.0
	fs.cons = []pgstore.CapabilityConstraint{
		{Subject: "dormant:dci_sell_high_covered_call@binance", ValueNum: &one},
		{Subject: "custody_share", ValueNum: f64(0.34)},
		{Subject: "custody_redeemable", Qualifier: "always"},
		{Subject: "dci_call_otm_bound", ValueNum: f64(1.10)},
		{Subject: "dci_put_otm_bound", ValueNum: f64(1.00)},
		{Subject: "dci_put_otm_k", ValueNum: f64(1.5)},
		{Subject: "dci_call_otm_k", ValueNum: f64(1.5)},
		{Subject: "dci_tenor_max_days", ValueNum: f64(30)},
		{Subject: "dci_open_notional_cap_usdt", ValueNum: f64(1e9)},
	}
	fs.quotes = []pgstore.CexDCIQuote{{Venue: "binance", Underlying: "BTC", ProductRef: "PUTP", OptType: "PUT", InvestCcy: "USDT",
		OrderRef: "O1", Strike: f64(80000), APY: 0.1, MinAmount: f64(10)}}
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		if strings.Contains(r.URL.Path, "subscribe") {
			posts++
		}
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := &Executor{SigmaFn: testSigma, Store: fs, BnKey: "k", BnSec: "s", Sapi: srv.URL,
		Client: srv.Client(), Now: time.Now,
		PriceFn:   func(context.Context, string) (float64, error) { return 100000, nil },
		BalanceFn: func(context.Context, string) (float64, error) { return 100.0, nil },
		EarnFn:    func(context.Context, string) ([]EarnPosition, error) { return nil, nil }}
	fs.addProposal("dci_buy_low")                // 次席（旧 id）
	fs.addProposal("dci_sell_high_covered_call") // 最新=休眠
	msg, err := e.Go(context.Background())
	if err != nil {
		t.Fatalf("裸 go 败：%v", err)
	}
	if !strings.Contains(msg, "dci_buy_low") {
		t.Fatalf("未取次席：%s", msg)
	}
	if posts < 1 {
		t.Fatal("次席应执行申购")
	}
}

// DormantPairs 解析锚：前缀+value_num=1 才生效。
func TestD204_DormantPairsParse(t *testing.T) {
	one, zero := 1.0, 0.0
	m := pgstore.DormantPairs([]pgstore.CapabilityConstraint{
		{Subject: "dormant:a@b", ValueNum: &one},
		{Subject: "dormant:c@d", ValueNum: &zero},
		{Subject: "dormant:e@f", ValueNum: nil},
		{Subject: "custody_share", ValueNum: &one},
	})
	if !m["a@b"] || m["c@d"] || m["e@f"] || m["custody_share"] || len(m) != 1 {
		t.Fatalf("解析错：%v", m)
	}
}
