package autotrade

import (
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
)

// TestMaybePublishFundingNeed_DailyThrottle 验证每日每组合至多 1 条（D-072）：
// 同组合同日第二次调用不发布；topic 订阅者只收到 1 条。
// SwapArbFundingNeedUSD 在无 acctMon 时返回 needUSD=0（fail-safe），不影响节流逻辑验证。
func TestMaybePublishFundingNeed_DailyThrottle(t *testing.T) {
	topic := bus.NewTopic[dashboard.FundingNeedNotice]()
	ch, cancel := topic.Subscribe()
	defer cancel()
	svc := &dashboard.Service{}
	svc.SetFundingNeedTopic(topic)
	e := &Engine{service: svc, fundingNeedSent: make(map[string]string)}

	e.maybePublishFundingNeed("AUDJPY", "529:mt5", "277:mt5", "balance $100 < min notional $500")
	e.maybePublishFundingNeed("AUDJPY", "529:mt5", "277:mt5", "balance $90 < min notional $500")

	// 第一条应收到。
	select {
	case got := <-ch:
		if got.Canonical != "AUDJPY" || got.VenueLong != "529:mt5" || got.VenueShort != "277:mt5" {
			t.Fatalf("notice fields mismatch: %+v", got)
		}
		if got.NeedUSD != 0 {
			t.Fatalf("want needUSD=0 (no acctMon), got %v", got.NeedUSD)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for first notice")
	}
	// 第二条应被节流丢弃——channel 无新消息。
	select {
	case got := <-ch:
		t.Fatalf("expected throttle to drop 2nd notice, got %+v", got)
	case <-time.After(100 * time.Millisecond):
		// 预期：无消息。
	}
	// 不同组合应独立发布。
	e.maybePublishFundingNeed("EURUSD", "529:mt5", "277:mt5", "balance $50 < min notional $300")
	select {
	case got := <-ch:
		if got.Canonical != "EURUSD" {
			t.Fatalf("want EURUSD, got %s", got.Canonical)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for second combo notice")
	}
}
