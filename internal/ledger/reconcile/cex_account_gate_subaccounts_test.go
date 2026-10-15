// cex_account_gate_subaccounts_test.go —— gate uni-lend 解析锚（自
// cex_account_subaccounts_test.go 按 venue 随迁——check-lines 拆分，零语义变化）。
package reconcile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// D-523④：venue 实拍字段=amount/lent_amount/frozen_amount（current_amount 恒空旧解恒 0）。
func TestParseGateUniLends_RealFields(t *testing.T) {
	body := []byte(`[{"currency":"USDT","amount":"2000","lent_amount":"1999.6412","frozen_amount":"0","min_rate":"0.0001"},
	                 {"currency":"GT","amount":"5","lent_amount":"5","frozen_amount":"0"}]`)
	usdt, hs, err := parseGateUniLends(body)
	if err != nil {
		t.Fatal(err)
	}
	if usdt != 1999.6412 {
		t.Fatalf("usdt=%v", usdt)
	}
	if len(hs) != 2 || hs[0].Ccy != "USDT" || hs[0].Qty != 1999.6412 || hs[1].UsdtValue != 0 {
		t.Fatalf("holdings=%+v", hs)
	}
	// 空出借/旧误字段 → 零值不臆造。
	usdt, hs, err = parseGateUniLends([]byte(`[{"currency":"USDT","current_amount":"1999.6"}]`))
	if err != nil || usdt != 0 || len(hs) != 0 {
		t.Fatalf("empty: usdt=%v hs=%v err=%v", usdt, hs, err)
	}
}

// TestCEXAccount_GateEarn_DualStructured 同型漏读补齐锚（okx DCI 10 USDT 零行实证同族）：
// gate earn 池=uni-lends 出借 + dual INIT 在仓 + structured SUCCESS 在仓三源并计；
// 终态（SETTLEMENT_SUCCESS/DONE）剔除；状态空串 fail-closed。
func TestCEXAccount_GateEarn_DualStructured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/earn/uni/lends":
			fmt.Fprint(w, `[{"currency":"USDT","amount":"2000","lent_amount":"1999.64","frozen_amount":"0"}]`)
		case "/earn/dual/orders":
			fmt.Fprint(w, `[
				{"id":1,"status":"INIT","invest_currency":"USDT","invest_amount":"50"},
				{"id":2,"status":"SETTLEMENT_SUCCESS","invest_currency":"USDT","invest_amount":"999"},
				{"id":3,"status":"SETTLEMENT_PROCESSING","invest_currency":"BTC","invest_amount":"0.01"}]`)
		case "/earn/structured/orders":
			fmt.Fprint(w, `[
				{"id":7,"status":"SUCCESS","lock_coin":"USDT","amount":"30"},
				{"id":8,"status":"DONE","lock_coin":"USDT","amount":"888"}]`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("gate", "earn", "key", "secret", "", srv.URL, "", false)
	acct, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("gate earn: %v", err)
	}
	// USDT=1999.64+50+30=2079.64，BTC=0.01 分币行；equity=Σ原生=2079.65。
	per := map[string]float64{}
	for _, h := range acct.Holdings {
		per[h.Ccy] = h.Qty
	}
	if d := per["USDT"] - 2079.64; d < -0.001 || d > 0.001 {
		t.Fatalf("USDT want 2079.64, got %v (holdings=%+v)", per["USDT"], acct.Holdings)
	}
	if per["BTC"] != 0.01 {
		t.Fatalf("BTC want 0.01, got %v", per["BTC"])
	}
	if acct.Currency != "MULTI" {
		t.Fatalf("currency want MULTI, got %q", acct.Currency)
	}
}

// TestCEXAccount_GateEarn_EmptyStatusFailClosed 状态空串行 fail-closed（禁静默过滤）。
func TestCEXAccount_GateEarn_EmptyStatusFailClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/earn/uni/lends":
			fmt.Fprint(w, `[]`)
		case "/earn/dual/orders":
			fmt.Fprint(w, `[{"id":9,"invest_currency":"USDT","invest_amount":"10"}]`)
		case "/earn/structured/orders":
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	q := NewCEXAccountQuerier("gate", "earn", "key", "secret", "", srv.URL, "", false)
	if _, err := q.Account(context.Background()); err == nil {
		t.Fatal("状态空串行应 fail-closed 返错")
	}
}
