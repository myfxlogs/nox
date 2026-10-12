// d434_okx_buffer_test.go —— D-434 T6（P2 主锚）：okx buffer 三层回落。
// 顶层 mgnRatio 空 + details[USDT] 供 mmr/mgnRatio（pos502 实采形态）。
package reconcile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func d434OKXServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/balance" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func d434OKXBuffer(t *testing.T, body string) float64 {
	t.Helper()
	srv := d434OKXServer(t, body)
	q := NewCEXAccountQuerier("okx", "unified", "k", "s", "p", srv.URL, "", false)
	info, err := q.Account(context.Background())
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	return info.BufferMultiple
}

// TestD434_OKXBuffer_Fallbacks T6：①details mmr>0 → eq/Σmmr（pos502 实参 eq=1515.9/
// mmr=17.45→86.87）②mmr 缺 detail mgnRatio → /100 ③details 全空顶层供值 → /100
// ④全空 → 0=未采。红=旧码只读顶层 → ①②全落 0。
func TestD434_OKXBuffer_Fallbacks(t *testing.T) {
	// ① 顶层空 + details[USDT] mmr/mgnRatio 双供 → eq/mmr 首选（非 mgnRatio/100）
	got := d434OKXBuffer(t, `{"code":"0","data":[{"mgnRatio":"","details":[
		{"ccy":"USDT","eqUsd":"1515.9","cashBal":"600","mmr":"17.45","mgnRatio":"77.2"}
	]}]}`)
	if got < 86.8 || got > 87.0 {
		t.Fatalf("① buffer=%.3f want≈86.87（eq/Σmmr）", got)
	}
	// ② mmr 缺 + detail mgnRatio=77.2 → 0.772
	got = d434OKXBuffer(t, `{"code":"0","data":[{"mgnRatio":"","details":[
		{"ccy":"USDT","eqUsd":"1515.9","cashBal":"600","mgnRatio":"77.2"}
	]}]}`)
	if got < 0.77 || got > 0.78 {
		t.Fatalf("② buffer=%.4f want≈0.772（detail mgnRatio/100）", got)
	}
	// ③ details 无 USDT margin 字段 + 顶层 mgnRatio=50 → 0.5（原行为保留）
	got = d434OKXBuffer(t, `{"code":"0","data":[{"mgnRatio":"50","details":[
		{"ccy":"USDT","eqUsd":"100","cashBal":"100"}
	]}]}`)
	if got < 0.49 || got > 0.51 {
		t.Fatalf("③ buffer=%.4f want≈0.5（顶层回落）", got)
	}
	// ④ 全空 → 0=未采（诚实 data_stale）
	if got = d434OKXBuffer(t, `{"code":"0","data":[{"mgnRatio":"","details":[
		{"ccy":"USDT","eqUsd":"100","cashBal":"100"}
	]}]}`); got != 0 {
		t.Fatalf("④ buffer=%.4f want 0（未采诚实）", got)
	}
}
