// d053_actuals_test.go —— D-053 ①②验收锚：zh 固定样本解析（中文事件名/holiday 归一/
// FullDate=UTC）+ actual 空→非空推送一次且二次不重推（seed 防历史重推 + sweep 判定）。
// R1③：T3 改驱动 runActualsLoop（rearm 信号即扫——迟到 actual 立推且仅一条）。
package macro

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"nox/internal/store/pgstore"
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

// d053FakeStore macro.Store 最小 fake（actuals 推送判定链；mu 护 goroutine 驱动径）。
type d053FakeStore struct {
	mu      sync.Mutex
	events  []pgstore.MacroEvent
	alerts  []string
	now     time.Time
	hiCalls int
}

func (s *d053FakeStore) NextHighEventAt(_ context.Context, now time.Time) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.events {
		if e.Importance == "high" && e.EventTime.After(now) {
			return e.EventTime, true, nil
		}
	}
	return time.Time{}, false, nil
}
func (s *d053FakeStore) MacroHighEventsIn(_ context.Context, from, to time.Time, _ []string) ([]pgstore.MacroEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hiCalls++ // 测试同步点：seed 查询已发→可落 actual（否则 seed 快照吞迟到值）
	var out []pgstore.MacroEvent
	for _, e := range s.events {
		if e.Importance == "high" && !e.EventTime.Before(from) && !e.EventTime.After(to) {
			out = append(out, e)
		}
	}
	return out, nil
}
func (s *d053FakeStore) InsertAlertDedup(_ context.Context, level, msg string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alerts = append(s.alerts, level+"|"+msg)
	return nil
}

// setActual 测试侧写 actual 值（loop goroutine 并发读——经 mu 同步）。
func (s *d053FakeStore) setActual(id int64, val string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.events {
		if s.events[i].ID == id {
			s.events[i].Actual = val
		}
	}
}

// macroCalls 播种查询次数（T3 时序同步：首调返回前即记——快照口径已锁）。
func (s *d053FakeStore) macroCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hiCalls
}

// alertCount/alertMsgs 断言读口（锁内快照）。
func (s *d053FakeStore) alertCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.alerts)
}
func (s *d053FakeStore) alertMsgs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.alerts...)
}
func (s *d053FakeStore) UpsertMacroEvents(context.Context, []pgstore.MacroEvent) (int, error) {
	return 0, nil
}
func (s *d053FakeStore) MacroEventsBetween(context.Context, time.Time, time.Time) ([]pgstore.MacroEvent, error) {
	return nil, nil
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
	if st.alertCount() != 0 {
		t.Fatalf("seed 阶段历史行不应推: %v", st.alertMsgs())
	}

	// actual 出值 → sweep 推一次。
	st.setActual(2, "170K")
	sweepActuals(context.Background(), d, seen, st.now)
	if msgs := st.alertMsgs(); len(msgs) != 1 || !containsStr(msgs[0], "实际 170K（预期 163K / 前值 168K）") {
		t.Fatalf("应推一次含三值文案: %v", msgs)
	}

	// 二次 sweep（值未变）不重推。
	sweepActuals(context.Background(), d, seen, st.now)
	if st.alertCount() != 1 {
		t.Fatalf("二次 sweep 不应重推: %v", st.alertMsgs())
	}
}

// TestD053_ActualLateArrivalViaRearm R1③ T3：驱动 runActualsLoop——无未来事件停在
// rearm 等；actual 迟到落库后 rearm 信号即扫推送（不等下个事件唤醒）；再 rearm 不重推
// （alerts 恰一条）。seed 在首迭代无条件跑——2h 前空行记基线，迟到值空→非空命中。
func TestD053_ActualLateArrivalViaRearm(t *testing.T) {
	now := time.Now()
	st := &d053FakeStore{now: now}
	st.events = append(st.events, pgstore.MacroEvent{ID: 3, Importance: "high",
		EventTime: now.Add(-2 * time.Hour), Currency: "USD", Name: "迟到事件"})
	d := Deps{Store: st, Now: func() time.Time { return st.now }}
	rearm := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runActualsLoop(ctx, d, &http.Client{}, rearm)

	// 等 seed 播种快照完成（MacroHighEventsIn 首调=快照已锁空值基线）——
	// 否则 actual 抢在 seed 前落地会被记成「历史非空」吞掉（时序竞态非语义）。
	for i := 0; i < 300 && st.macroCalls() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	// actual 迟到（4h 兜底刷进库场景）→ rearm 即扫应推送。
	st.setActual(3, "9.9")
	rearm <- struct{}{}
	deadline := time.Now().Add(3 * time.Second)
	for st.alertCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("rearm 后迟到 actual 未推送")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if msgs := st.alertMsgs(); !containsStr(msgs[0], "迟到事件") {
		t.Fatalf("推送应含事件名: %v", msgs)
	}

	// 再 rearm（值未变）→ seen 已记 → 不重推。
	rearm <- struct{}{}
	time.Sleep(150 * time.Millisecond)
	if st.alertCount() != 1 {
		t.Fatalf("二次 rearm 不应重推: %v", st.alertMsgs())
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
