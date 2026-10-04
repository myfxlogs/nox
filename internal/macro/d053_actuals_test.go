// d053_actuals_test.go —— D-053 ①②验收锚：zh 固定样本解析（中文事件名/holiday 归一/
// FullDate=UTC）+ actual 空→非空推送一次且二次不重推（seed 防历史重推 + sweep 判定）。
package macro

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// TestD053_ZhSampleParse ①：zh 端点固定样本解析——中文事件名原样入库、none∧三空→holiday
// （D-029 ④归一在 zh 行为同样命中）、FullDate=UTC 坐标、Importance 大小写归一。
func TestD053_ZhSampleParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"Id":300017,"EventName":"国庆节","Importance":"none","CurrencyCode":"CNY","FullDate":"2026-10-01T00:00:00","ActualValue":"","ForecastValue":"","PreviousValue":""},
			{"Id":9001,"EventName":"非农就业人数","Importance":"high","CurrencyCode":"USD","FullDate":"2026-10-02T12:30:00","ActualValue":"170K","ForecastValue":"163K","PreviousValue":"168K"},
			{"Id":9002,"EventName":"欧元区CPI年率","Importance":"High","CurrencyCode":"EUR","FullDate":"2026-10-02T09:00:00","ActualValue":"","ForecastValue":"2.1%","PreviousValue":"2.0%"}
		]`))
	}))
	defer srv.Close()

	events, err := FetchMQL5Calendar(context.Background(), srv.Client(), srv.URL,
		time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("want 3 events, got %d", len(events))
	}
	if events[0].Name != "国庆节" || events[0].Importance != "holiday" {
		t.Fatalf("zh 假日归一: %+v", events[0])
	}
	nfp := events[1]
	if nfp.Name != "非农就业人数" || nfp.Importance != "high" || nfp.Actual != "170K" || nfp.Currency != "USD" {
		t.Fatalf("zh high 行: %+v", nfp)
	}
	// FullDate=UTC：12:30 裸串=UTC 午夜坐标（站点不翻时区——对拍结论）。
	if got := nfp.EventTime.UTC().Format("2006-01-02T15:04:05"); got != "2026-10-02T12:30:00" {
		t.Fatalf("FullDate 应 UTC 原样: %s", got)
	}
}

// d053FakeStore macro.Store 最小 fake（actuals 推送判定链）。
type d053FakeStore struct {
	events []pgstore.MacroEvent
	alerts []string
	now    time.Time
}

func (s *d053FakeStore) NextHighEventAt(_ context.Context, now time.Time) (time.Time, bool, error) {
	for _, e := range s.events {
		if e.Importance == "high" && e.EventTime.After(now) {
			return e.EventTime, true, nil
		}
	}
	return time.Time{}, false, nil
}
func (s *d053FakeStore) MacroHighEventsIn(_ context.Context, from, to time.Time, _ []string) ([]pgstore.MacroEvent, error) {
	var out []pgstore.MacroEvent
	for _, e := range s.events {
		if e.Importance == "high" && !e.EventTime.Before(from) && !e.EventTime.After(to) {
			out = append(out, e)
		}
	}
	return out, nil
}
func (s *d053FakeStore) InsertAlertDedup(_ context.Context, level, msg string, _ time.Duration) error {
	s.alerts = append(s.alerts, level+"|"+msg)
	return nil
}
func (s *d053FakeStore) UpsertMacroEvents(context.Context, []pgstore.MacroEvent) (int, error) {
	return 0, nil
}
func (s *d053FakeStore) UpsertCotPositions(context.Context, []pgstore.CotPosition) (int, error) {
	return 0, nil
}
func (s *d053FakeStore) UpsertFredObs(context.Context, []pgstore.FredObs) (int, error) {
	return 0, nil
}
func (s *d053FakeStore) MacroEventsBetween(context.Context, time.Time, time.Time) ([]pgstore.MacroEvent, error) {
	return nil, nil
}
func (s *d053FakeStore) MaxCotReportDate(context.Context) (time.Time, error) {
	return time.Time{}, nil
}

// TestD053_ActualPushOnce ②：actual 空→非空推送一次；再 sweep 不重推；seed 播种
// 历史非空行不推（防重启重推）。
func TestD053_ActualPushOnce(t *testing.T) {
	now := time.Now()
	st := &d053FakeStore{now: now}
	// 历史行：3h 前 actual 已非空（重启/上线场景）——seed 静默。
	st.events = append(st.events, pgstore.MacroEvent{ID: 1, Importance: "high",
		EventTime: now.Add(-3 * time.Hour), Currency: "USD", Name: "已出值事件", Actual: "5.0"})
	// 活跃行：10min 前发布（initialFresh 窗内）actual 仍空。
	st.events = append(st.events, pgstore.MacroEvent{ID: 2, Importance: "high",
		EventTime: now.Add(-10 * time.Minute), Currency: "USD", Name: "非农就业", Forecast: "163K", Previous: "168K"})

	d := Deps{Store: st, Now: func() time.Time { return st.now }}
	seen := map[int64]string{}
	seedSeen(context.Background(), d, seen, st.now)
	if len(st.alerts) != 0 {
		t.Fatalf("seed 阶段历史行不应推: %v", st.alerts)
	}

	// actual 出值 → sweep 推一次。
	st.events[1].Actual = "170K"
	sweepActuals(context.Background(), d, seen, st.now)
	if len(st.alerts) != 1 || !containsStr(st.alerts[0], "实际 170K（预期 163K / 前值 168K）") {
		t.Fatalf("应推一次含三值文案: %v", st.alerts)
	}

	// 二次 sweep（值未变）不重推。
	sweepActuals(context.Background(), d, seen, st.now)
	if len(st.alerts) != 1 {
		t.Fatalf("二次 sweep 不应重推: %v", st.alerts)
	}
}

// TestD053_ActualLateArrivalViaRearm ②兜底路径：4h ticker（rearm 信号）拉回的迟到
// actual 同样走 sweep 推送（事件时点已过 initialFresh 窗仍推——空→非空判定为准）。
func TestD053_ActualLateArrivalViaRearm(t *testing.T) {
	now := time.Now()
	st := &d053FakeStore{now: now}
	st.events = append(st.events, pgstore.MacroEvent{ID: 3, Importance: "high",
		EventTime: now.Add(-2 * time.Hour), Currency: "USD", Name: "迟到事件"})
	d := Deps{Store: st, Now: func() time.Time { return st.now }}
	seen := map[int64]string{}
	seedSeen(context.Background(), d, seen, st.now)

	st.events[0].Actual = "9.9" // 4h 兜底刷进库
	sweepActuals(context.Background(), d, seen, st.now)
	if len(st.alerts) != 1 || !containsStr(st.alerts[0], "迟到事件") {
		t.Fatalf("迟到 actual 应经 sweep 推送: %v", st.alerts)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
