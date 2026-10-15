// binance_dci_pos_status_test.go —— DCI 持仓字段名返修锚（venue 实拍键=purchaseStatus）：
// ①SETTLED 行不计 earn 池（status 键恒空穿透 HOLDING 过滤=1441 虚计实证）；
// ②purchaseStatus 键空=fail-closed 返错。
package reconcile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func dciPosQuerier(t *testing.T, body string) (*CEXAccountQuerier, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/dci/product/positions") {
			t.Errorf("want /v1/dci/product/positions, got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
	return NewCEXAccountQuerier("binance", "earn", "k", "s", "", srv.URL, "", false), srv.Close
}

func TestBinanceDCIPos_PurchaseStatus(t *testing.T) {
	q, closeFn := dciPosQuerier(t, `{"data":[
		{"id":"18549966","instrumentName":"BTCUSDT","productRef":"P1","purchaseStatus":"PURCHASE_SUCCESS","investCoin":"USDT","subscriptionAmount":"358.05"},
		{"id":"18549738","instrumentName":"BTCUSDT","productRef":"P1","purchaseStatus":"SETTLED","investCoin":"USDT","subscriptionAmount":"542"},
		{"id":"18538465","instrumentName":"BTCUSDT","productRef":"P1","purchaseStatus":"SETTLED","investCoin":"USDT","subscriptionAmount":"358"},
		{"id":"18537557","instrumentName":"BTCUSDT","productRef":"P1","purchaseStatus":"SETTLED","investCoin":"USDT","subscriptionAmount":"541"}
	]}`)
	defer closeFn()
	per := map[string]float64{}
	add := func(c string, q2 float64) { per[c] += q2 }
	refs, err := q.dciPositionsEarn(context.Background(), q.baseURL, http.Header{}, add)
	if err != nil {
		t.Fatal(err)
	}
	if per["USDT"] != 358.05 {
		t.Fatalf("SETTLED 行混入 earn 池: %v", per)
	}
	if len(refs) != 3 {
		t.Fatalf("refs 应=在仓行三件引用: %v", refs)
	}
}

func TestBinanceDCIPos_StatusKeyEmpty_FailClosed(t *testing.T) {
	q, closeFn := dciPosQuerier(t, `{"data":[
		{"id":"x1","investCoin":"USDT","subscriptionAmount":"10"}
	]}`)
	defer closeFn()
	add := func(string, float64) {}
	if _, err := q.dciPositionsEarn(context.Background(), q.baseURL, http.Header{}, add); err == nil ||
		!strings.Contains(err.Error(), "purchaseStatus") {
		t.Fatalf("purchaseStatus 键空应 fail-closed 返错: %v", err)
	}
}
