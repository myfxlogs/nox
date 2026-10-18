// d203_test.go —— D-203 锚：尘埃足量续行零 POST/productId 空跳行+note/
// redeem 败 err 内嵌实发参回声/撤 type=FAST。
package dciexec

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 尘埃闸：spotFree ≥ qty×(1-1e-6) → 判足量直接进申购，零赎回 POST。
func TestD203_DustNoRedeem(t *testing.T) {
	fs := newFake()
	redeemCalled := false
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
	spot := 0.0068 * (1 - 5e-7) // 池=现货 0.0068（赚币 0），现货差 5e-7 相对=尘埃
	e := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma, Store: fs, BnKey: "k", BnSec: "s", Sapi: srv.URL,
		Client: srv.Client(), Now: time.Now,
		PriceFn: func(context.Context, string) (float64, error) { return 100000, nil },
		BalanceFn: func(context.Context, string) (float64, error) {
			return spot, nil
		},
		EarnFn: func(context.Context, string) ([]EarnPosition, error) { return nil, nil },
		RedeemFn: func(context.Context, string, float64) error {
			redeemCalled = true
			return nil
		}}
	pid := fs.addProposal("dci_sell_high_covered_call")
	if _, err := e.GoProposal(context.Background(), pid); err != nil {
		t.Fatal(err)
	}
	if redeemCalled {
		t.Fatal("尘埃缺口不应触发赎回")
	}
	if posts < 1 {
		t.Fatal("尘埃足量应续行至申购 POST")
	}
}

// 跳行 note + 参数回声：productId 空行跳过且 holding gap 带 note；
// redeem 败 err 内嵌 redeem(productId=… amount=… need=…)。
func TestD203_RedeemEchoAndSkip(t *testing.T) {
	fs := newFake()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	e := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma, Store: fs, BnKey: "k", BnSec: "s", Sapi: srv.URL,
		Client: srv.Client(), Now: time.Now,
		PriceFn:   func(context.Context, string) (float64, error) { return 100000, nil },
		BalanceFn: func(context.Context, string) (float64, error) { return 0.001, nil },
		EarnFn: func(context.Context, string) ([]EarnPosition, error) {
			return []EarnPosition{{ProductID: "EP2", Amount: 0.5}}, nil
		},
		RedeemFn: func(_ context.Context, pid string, amt float64) error {
			return fmt.Errorf("venue 拒赎 -4005")
		}}
	_, err := e.GoProposal(context.Background(), fs.addProposal("dci_sell_high_covered_call"))
	if err == nil || !strings.Contains(err.Error(), `redeem(productId="EP2"`) ||
		!strings.Contains(err.Error(), "amount=") || !strings.Contains(err.Error(), "need=") ||
		!strings.Contains(err.Error(), "venue 拒赎 -4005") {
		t.Fatalf("回声缺：%v", err)
	}
	// redeemShortfall 跳行单元锚：productId 空+NaN 量+locked 三跳行→notes 齐。
	e3 := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma, Now: time.Now,
		EarnFn: func(context.Context, string) ([]EarnPosition, error) {
			return []EarnPosition{
				{ProductID: "", Amount: 0.5},
				{ProductID: "EPN", Amount: math.NaN()},
				{ProductID: "EPL", Amount: 9, Locked: true},
			}, nil
		}}
	done, notes, err := e3.redeemShortfall(context.Background(), "BTC", 1)
	if err != nil || done != 0 || len(notes) != 2 {
		t.Fatalf("跳行锚：done=%v notes=%v err=%v", done, notes, err)
	}
	if !strings.Contains(notes[0], "productId 空行") || !strings.Contains(notes[1], "异常量") {
		t.Fatalf("note 文案缺：%v", notes)
	}
	// 跳行 note 场景：全行被跳→holding gap 带 note。
	fs2 := newFake()
	e2 := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma, Store: fs2, BnKey: "k", BnSec: "s", Sapi: srv.URL,
		Client: srv.Client(), Now: time.Now,
		PriceFn:   func(context.Context, string) (float64, error) { return 100000, nil },
		BalanceFn: func(context.Context, string) (float64, error) { return 0.001, nil },
		EarnFn: func(context.Context, string) ([]EarnPosition, error) {
			return []EarnPosition{{ProductID: "", Amount: 0.5}}, nil
		}}
	_, err2 := e2.GoProposal(context.Background(), fs2.addProposal("dci_sell_high_covered_call"))
	if err2 == nil || !strings.Contains(err2.Error(), "productId 空行") {
		t.Fatalf("跳行 note 缺：%v", err2)
	}
}

// 撤 type=FAST：赎回 POST query 不含 type 参。
func TestD203_NoTypeFast(t *testing.T) {
	var gotQ string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQ = r.URL.RawQuery
		fmt.Fprint(w, `{"success":true}`)
	}))
	defer srv.Close()
	e := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma, BnKey: "k", BnSec: "s", Sapi: srv.URL, Client: srv.Client(), Now: time.Now}
	if err := e.redeemFlexible(context.Background(), "P1", 0.01); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotQ, "type=") {
		t.Fatalf("type=FAST 应撤：%s", gotQ)
	}
	if !strings.Contains(gotQ, "productId=P1") || !strings.Contains(gotQ, "amount=0.01000000") {
		t.Fatalf("实发参缺：%s", gotQ)
	}
}
