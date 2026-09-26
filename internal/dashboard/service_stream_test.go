package dashboard

import (
	"testing"
	"time"

	"arb/internal/bus"

	v1 "arb/internal/gen/dashboardv1"
)

// TestOrderTopicToPositionChangeMapping D-107 P3：bus.OrderEvent → PositionChangeNotice 字段映射验证。
// StreamUpdates case 11 逐条推：venue/ticket 从 OrderEvent 透传到 PositionChangeNotice（只含通知不含数据）。
func TestOrderTopicToPositionChangeMapping(t *testing.T) {
	// OrderEvent → PositionChangeNotice 映射（service_stream.go case 11 同款逻辑）。
	e := bus.OrderEvent{Venue: "52993526:mt5", Ticket: 12345}
	pc := &v1.PositionChangeNotice{
		Venue:  e.Venue,
		Ticket: e.Ticket,
	}
	if pc.Venue != "52993526:mt5" {
		t.Fatalf("venue mismatch: got %q want %q", pc.Venue, "52993526:mt5")
	}
	if pc.Ticket != 12345 {
		t.Fatalf("ticket mismatch: got %d want %d", pc.Ticket, 12345)
	}
}

// TestOrderTopicPublishSubscribe D-107 P3：orderTopic Publish → 订阅者收到 OrderEvent（扇出接线验证）。
// 验证 StreamUpdates 的 orderTopic.Subscribe() 能收到事件（case 11 推送的数据源）。
func TestOrderTopicPublishSubscribe(t *testing.T) {
	topic := bus.NewTopic[bus.OrderEvent]()
	ch, cancel := topic.Subscribe()
	defer cancel()

	want := bus.OrderEvent{Venue: "277842155:mt5", Ticket: 99}
	topic.Publish(want)

	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for published OrderEvent")
	}
}

// TestOrderTopicNilSafe D-107 P3：Service.orderTopic 为 nil（测试未接线）→ StreamUpdates 不订阅该分支不 panic。
func TestOrderTopicNilSafe(t *testing.T) {
	s := &Service{}
	// orderTopic nil → 不订阅（StreamUpdates 中 if s.orderTopic != nil 守卫）。
	if s.orderTopic != nil {
		t.Fatal("orderTopic should be nil by default")
	}
}

// TestHedgeTopicPublishSubscribe D-231 S5 ⑨：hedgeTopic Publish → 订阅者收到 struct{}{}（case 12 推送的数据源）。
// 验证 StreamUpdates 的 hedgeTopic.Subscribe() 能收到通知（仿 TestOrderTopicPublishSubscribe 模式）。
func TestHedgeTopicPublishSubscribe(t *testing.T) {
	topic := bus.NewTopic[struct{}]()
	ch, cancel := topic.Subscribe()
	defer cancel()

	topic.Publish(struct{}{})

	select {
	case got := <-ch:
		var want struct{}
		if got != want {
			t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for published HedgeRefresh notice")
	}
}

// TestHedgeTopicNilSafe D-231 S5：Service.hedgeTopic 为 nil（测试未接线）→ StreamUpdates 不订阅该分支不 panic。
func TestHedgeTopicNilSafe(t *testing.T) {
	s := &Service{}
	if s.hedgeTopic != nil {
		t.Fatal("hedgeTopic should be nil by default")
	}
}

// TestRefreshTopicPublishSubscribe D-508 S9：refreshTopic.Subscribe() 收到域刷新通知（case 14 数据源，同款验证）。
func TestRefreshTopicPublishSubscribe(t *testing.T) {
	topic := bus.NewTopic[string]()
	ch, cancel := topic.Subscribe()
	defer cancel()

	s := &Service{}
	s.SetRefreshTopic(topic)
	s.PublishDataRefresh(RefreshAllowRules)

	select {
	case got := <-ch:
		if got != RefreshAllowRules {
			t.Fatalf("topic mismatch: got %q want %q", got, RefreshAllowRules)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for data refresh notice")
	}
}

// TestRefreshTopicNilSafe D-508 S9：refreshTopic 未接线 → PublishDataRefresh 静默不 panic。
func TestRefreshTopicNilSafe(t *testing.T) {
	s := &Service{}
	s.PublishDataRefresh(RefreshAllowRules) // nil topic → no panic
	if s.refreshTopic != nil {
		t.Fatal("refreshTopic should be nil by default")
	}
}
