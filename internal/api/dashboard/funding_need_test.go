package dashboard

import (
	"testing"
	"time"

	"arb/internal/data/bus"
)

// TestFundingNeedToProto 验证 FundingNeedNotice → proto 信封字段映射（D-072 case 10）。
// D-154 返修②：含 AliasLong/AliasShort 透传断言 + 空 alias 子例（回落逻辑在前端，proto 仅透传空串）。
func TestFundingNeedToProto(t *testing.T) {
	n := FundingNeedNotice{
		Canonical:  "AUDJPY",
		VenueLong:  "529:mt5",
		VenueShort: "277:mt5",
		Reason:     "balance $100.00 < min notional $500.00",
		NeedUSD:    400.0,
		Ts:         time.Unix(1700000000, 0),
		AliasLong:  "ICMarkets-Live10",
		AliasShort: "FxPro-MT5 Demo",
	}
	p := fundingNeedToProto(n)
	if p.Canonical != "AUDJPY" || p.VenueLong != "529:mt5" || p.VenueShort != "277:mt5" {
		t.Fatalf("venue fields mismatch: %+v", p)
	}
	if p.Reason != n.Reason {
		t.Fatalf("reason mismatch: got %q", p.Reason)
	}
	if p.NeedUsd != 400.0 {
		t.Fatalf("need_usd mismatch: got %v", p.NeedUsd)
	}
	if p.Ts == nil || p.Ts.Seconds != 1700000000 {
		t.Fatalf("ts mismatch: %+v", p.Ts)
	}
	if p.AliasLong != "ICMarkets-Live10" || p.AliasShort != "FxPro-MT5 Demo" {
		t.Fatalf("alias mismatch: got long=%q short=%q", p.AliasLong, p.AliasShort)
	}
	// 空 alias 子例：proto 透传空串（前端 venueLabel 回落 splitVenue name）。
	pEmpty := fundingNeedToProto(FundingNeedNotice{Canonical: "X", VenueLong: "529:mt5", VenueShort: "277:mt5"})
	if pEmpty.AliasLong != "" || pEmpty.AliasShort != "" {
		t.Fatalf("empty alias should pass through as \"\", got long=%q short=%q", pEmpty.AliasLong, pEmpty.AliasShort)
	}
}

// TestPublishFundingNeed_TopicRoundTrip 验证 Service.PublishFundingNeed → topic 订阅者收到（D-072）。
// 未接线（nil topic）时静默丢弃不 panic。
func TestPublishFundingNeed_TopicRoundTrip(t *testing.T) {
	// nil topic：静默丢弃，不 panic。
	s := &Service{}
	s.PublishFundingNeed(FundingNeedNotice{Canonical: "X"})
	// 接线后：订阅者收到原样消息。
	topic := bus.NewTopic[FundingNeedNotice]()
	ch, cancel := topic.Subscribe()
	defer cancel()
	s.SetFundingNeedTopic(topic)
	want := FundingNeedNotice{Canonical: "AUDJPY", VenueLong: "529:mt5", VenueShort: "277:mt5", NeedUSD: 250.5}
	s.PublishFundingNeed(want)
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for published notice")
	}
}
