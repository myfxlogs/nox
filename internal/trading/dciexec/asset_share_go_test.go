// asset_share_go_test.go —— custody_share:<ccy> 资产域 cap 消费面锚
// （constraint#41「BTC 只能用 1/3」）：covered_call sizing=venue 池份额×
// 资产份额双闸取小——BTC 总持有 0.042×min(0.34,0.3333)→usable 0.014 锚；
// 无键行=不设限向后兼容（对照组 deposit 回 0.0142 原值）。
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

func assetShareGoFixture(t *testing.T, cons []pgstore.CapabilityConstraint) (*fakeStore, *string) {
	t.Helper()
	fs := newFake()
	fs.cons = append(fs.cons, cons...)
	var gotQ string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		if strings.Contains(r.URL.Path, "subscribe") {
			gotQ = r.URL.RawQuery
		}
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	t.Cleanup(srv.Close)
	e := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma, Store: fs, BnKey: "k", BnSec: "s", Sapi: srv.URL,
		Client: srv.Client(), Now: time.Now,
		PriceFn: func(context.Context, string) (float64, error) { return 100000, nil },
		BalanceFn: func(_ context.Context, asset string) (float64, error) {
			if asset == "BTC" {
				return 0.042, nil
			}
			return 1e6, nil
		},
		EarnFn: func(context.Context, string) ([]EarnPosition, error) { return nil, nil }}
	if _, err := e.GoProposal(context.Background(), fs.addProposal("dci_sell_high_covered_call")); err != nil {
		t.Fatalf("go 败: %v", err)
	}
	return fs, &gotQ
}

// BTC 0.042×0.3333→0.0139986 floor dec4=0.0139（资产键取小生效锚）。
func TestAssetShareCap_BtcThird(t *testing.T) {
	fs, gotQ := assetShareGoFixture(t, []pgstore.CapabilityConstraint{
		{Subject: "custody_share:BTC", Qualifier: "max", ValueNum: f64(0.3333)},
	})
	if !strings.Contains(*gotQ, "depositAmount=0.0139") {
		t.Fatalf("资产 cap 未生效（应 0.0139）: %s", *gotQ)
	}
	_ = fs
}

// 对照：无资产键→venue 份额原径 0.042×0.34=0.01428→0.0142（向后兼容锚）。
func TestAssetShareCap_NoKeyPassthrough(t *testing.T) {
	_, gotQ := assetShareGoFixture(t, nil)
	if !strings.Contains(*gotQ, "depositAmount=0.0142") {
		t.Fatalf("无键应走 venue 份额原径（应 0.0142）: %s", *gotQ)
	}
}
