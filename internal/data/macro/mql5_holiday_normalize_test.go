// d029_holiday_test.go —— D-029 ④：MQL5 假日归一（importance=none ∧ 三值全空 → holiday；
// raw 不改；有值/显 none 非假日 → 保持）。
package macro

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func mql5Srv(t *testing.T, events []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(events)
	}))
}

func TestD029_MQL5HolidayNormalize(t *testing.T) {
	events := []map[string]any{
		{"Id": 1, "EventName": "CNY 国庆", "Importance": "None", "CurrencyCode": "CNY", "FullDate": "2026-10-01T00:00:00"},
		{"Id": 2, "EventName": "NFP", "Importance": "High", "CurrencyCode": "USD", "FullDate": "2026-10-02T12:30:00", "ForecastValue": "150k", "PreviousValue": "140k"},
		{"Id": 3, "EventName": "ECO 发布", "Importance": "none", "CurrencyCode": "EUR", "FullDate": "2026-10-05T08:00:00", "ActualValue": "42"},
	}
	srv := mql5Srv(t, events)
	defer srv.Close()
	evs, err := FetchMQL5Calendar(context.Background(), srv.Client(), srv.URL, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("events=%d, want 3", len(evs))
	}
	if evs[0].Importance != "holiday" {
		t.Errorf("none∧三值全空应归 holiday, got %q", evs[0].Importance)
	}
	if evs[1].Importance != "high" {
		t.Errorf("high 保持, got %q", evs[1].Importance)
	}
	if evs[2].Importance != "none" {
		t.Errorf("none 有 Actual 值（非假日数据发布）保持 none, got %q", evs[2].Importance)
	}
	// raw 不改：原文 Importance 大小写保留
	var raw []map[string]any
	if err := json.Unmarshal(evs[0].Raw, &raw); err == nil {
		// raw 是单个 event 对象数组元素（mql5Event 序列化）；退化断言：
	}
	var single mql5Event
	if err := json.Unmarshal(evs[0].Raw, &single); err != nil {
		t.Fatalf("raw unmarshal: %v", err)
	}
	if single.Importance != "None" {
		t.Errorf("raw Importance 应保留原值 None（不改 raw），got %q", single.Importance)
	}
}
