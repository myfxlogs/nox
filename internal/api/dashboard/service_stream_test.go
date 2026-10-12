package dashboard

import (
	"testing"
	"time"

	"arb/internal/data/bus"
)

// TestOrderTopicPublishSubscribe D-107 P3：orderTopic Publish → 订阅者收到 OrderEvent（扇出接线验证）。
// orderTopic 仍是 bpMon.Kick 与 engine Kick 的数据源（D-571 包C case11 退役后不改总线语义）。
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
