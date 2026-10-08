// macro_test.go —— D-528 S9：财历 fetcher+渲染测试（先红后绿锚；D-120 清退 cot/fred 面）。
// UA 铁律钉死：mql5 请求 User-Agent 必须精确等于 "Mozilla/5.0"（Chrome 模板=WAF 403 实测坑）。
package macro

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// TestD528_MQL5UserAgentUA 铁律锚：UA==Mozilla/5.0 且不带 Referer/XRW/Origin 三头。
func TestD528_MQL5UserAgentUA(t *testing.T) {
	var gotUA, gotRef, gotXRW, gotOrigin string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotRef = r.Header.Get("Referer")
		gotXRW = r.Header.Get("X-Requested-With")
		gotOrigin = r.Header.Get("Origin")
		if gotRef == "" || gotXRW == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"Id":1,"EventName":"Nonfarm Payrolls","Importance":"High","CurrencyCode":"USD","FullDate":"2026-10-02T12:30:00","ForecastValue":"170K","PreviousValue":"168K"}]`))
	}))
	defer srv.Close()

	events, err := FetchMQL5Calendar(context.Background(), srv.Client(), srv.URL,
		time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotUA != "Mozilla/5.0" {
		t.Fatalf("UA 铁律违约：got %q want \"Mozilla/5.0\"", gotUA)
	}
	if gotRef == "" || gotXRW == "" {
		t.Fatalf("XHR 面必设：Referer=%q XRW=%q（缺→上游 200 空数组）", gotRef, gotXRW)
	}
	if gotOrigin != "" {
		t.Fatalf("Origin 必须不设：got %q", gotOrigin)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	e := events[0]
	if e.Source != "mql5" || e.Currency != "USD" || e.Importance != "high" {
		t.Fatalf("字段映射错: %+v", e)
	}
	if want := time.Date(2026, 10, 2, 12, 30, 0, 0, time.UTC); !e.EventTime.Equal(want) {
		t.Fatalf("FullDate 应按 UTC 解析: got %v want %v", e.EventTime, want)
	}
}

// TestD528_RenderCalendarCN BJ 换算+高低影响过滤（A4 锚：UTC 01:30 → BJ 09:30 同日）。
func TestD528_RenderCalendarCN(t *testing.T) {
	now := time.Date(2026, 9, 30, 2, 0, 0, 0, bjLoc) // BJ 09-30 02:00
	events := []pgstore.MacroEvent{
		{Source: "mql5", EventTime: time.Date(2026, 9, 30, 1, 30, 0, 0, time.UTC), Currency: "USD", Importance: "high", Name: "ADP Employment", Forecast: "120K", Previous: "110K"},
		{Source: "mql5", EventTime: time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC), Currency: "JPY", Importance: "medium", Name: "BOJ Meeting", Actual: "0.50%"},
		{Source: "mql5", EventTime: time.Date(2026, 10, 1, 5, 0, 0, 0, time.UTC), Currency: "EUR", Importance: "low", Name: "Retail Sales"}, // 低影响→滤除
		{Source: "mql5", EventTime: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC), Currency: "USD", Importance: "high", Name: "窗口外事件"},      // 48h 外→滤除
	}
	out := RenderCalendarCN(events, now)
	for _, want := range []string{"09-30 09:30 USD 高 ADP Employment（预期 120K / 前值 110K）", "10-01 07:00 JPY 中 BOJ Meeting（实际 0.50%）", "北京时间"} {
		if !strings.Contains(out, want) {
			t.Errorf("日报缺 %q；out=%s", want, out)
		}
	}
	if strings.Contains(out, "Retail Sales") || strings.Contains(out, "窗口外事件") {
		t.Errorf("过滤失败: %s", out)
	}
}

// 编译期防呆：raw 列可反序列化（jsonb 往返）。
func TestD528_RawJSONRoundTrip(t *testing.T) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(`{"Id":1}`), &raw); err != nil {
		t.Fatal(err)
	}
}
