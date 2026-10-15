// cex_account_gate_subaccounts_test.go —— gate uni-lend 解析锚（自
// cex_account_subaccounts_test.go 按 venue 随迁——check-lines 拆分，零语义变化）。
package reconcile

import "testing"

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
