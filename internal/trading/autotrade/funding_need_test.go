package autotrade

import (
	"context"
	"testing"
	"time"

	"arb/internal/api/dashboard"
	"arb/internal/data/bus"
	"arb/internal/risk/gate"
)

// setupFundingNeedEngine 构造带 DB store 的 Engine（autotrade_state 已设为指定 state）+ topic。
// D-159 W4a S7：maybePublishFundingNeed 加了 state 检查，需 DSN store + SetAutotradeState。
// 返回 (engine, topic) 供测试订阅。
func setupFundingNeedEngine(t *testing.T, state string) (*Engine, *bus.Topic[dashboard.FundingNeedNotice]) {
	t.Helper()
	s := testAccumStore(t)
	if _, err := s.Pool().Exec(context.Background(), `TRUNCATE positions, position_legs, autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// autotrade_state 是单行表（migration 插入初始行），TRUNCATE 后需 INSERT。
	if _, err := s.Pool().Exec(context.Background(),
		`INSERT INTO autotrade_state (state, reason, halted_on, updated_at) VALUES ($1, 'test', NULL, now())`,
		state); err != nil {
		t.Fatalf("insert autotrade_state: %v", err)
	}
	topic := bus.NewTopic[dashboard.FundingNeedNotice]()
	svc := &dashboard.Service{}
	svc.SetFundingNeedTopic(topic)
	return &Engine{
		store:           s,
		service:         svc,
		fundingNeedSent: make(map[string]string),
	}, topic
}

// TestMaybePublishFundingNeed_DailyThrottle 验证每日每组合至多 1 条（D-072）：
// 同组合同日第二次调用不发布；topic 订阅者只收到 1 条。
// D-159 W4a S7：需 DSN store + state=RUNNING（maybePublishFundingNeed 加了 state 检查）。
func TestMaybePublishFundingNeed_DailyThrottle(t *testing.T) {
	e, topic := setupFundingNeedEngine(t, gate.StateRunning)
	ch, cancel := topic.Subscribe()
	defer cancel()

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
		// D-154 返修②：aliasOrKey 填充断言——e.store 非 nil 但无 alias 映射时回落 venueKey。
		if got.AliasLong != "529:mt5" || got.AliasShort != "277:mt5" {
			t.Fatalf("alias mismatch (无 alias 映射 → 回落 venueKey): got long=%q short=%q", got.AliasLong, got.AliasShort)
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

// TestMaybePublishFundingNeed_CloseOnly_Skip D-159 W4a S7：state=CLOSE_ONLY → 不发弹窗。
func TestMaybePublishFundingNeed_CloseOnly_Skip(t *testing.T) {
	e, topic := setupFundingNeedEngine(t, gate.StateCloseOnly)
	ch, cancel := topic.Subscribe()
	defer cancel()

	e.maybePublishFundingNeed("AUDJPY", "529:mt5", "277:mt5", "sizing insufficient")

	select {
	case got := <-ch:
		t.Fatalf("CLOSE_ONLY should not publish funding need, got %+v", got)
	case <-time.After(100 * time.Millisecond):
		// 预期：无消息。
	}
}

// TestMaybePublishFundingNeed_Running_Publish D-159 W4a S7：state=RUNNING → 发弹窗。
func TestMaybePublishFundingNeed_Running_Publish(t *testing.T) {
	e, topic := setupFundingNeedEngine(t, gate.StateRunning)
	ch, cancel := topic.Subscribe()
	defer cancel()

	e.maybePublishFundingNeed("AUDJPY", "529:mt5", "277:mt5", "sizing insufficient")

	select {
	case got := <-ch:
		if got.Canonical != "AUDJPY" {
			t.Fatalf("want AUDJPY, got %s", got.Canonical)
		}
	case <-time.After(time.Second):
		t.Fatal("RUNNING should publish funding need, but timeout")
	}
}

// TestMaybePublishFundingNeed_StateError_Skip D-159 W4a S7：GetAutotradeState error → fail-closed 不发弹窗。
func TestMaybePublishFundingNeed_StateError_Skip(t *testing.T) {
	e, topic := setupFundingNeedEngine(t, gate.StateRunning)
	// TRUNCATE autotrade_state 使 GetAutotradeState 返回 error（no rows）。
	if _, err := e.store.Pool().Exec(context.Background(), `TRUNCATE autotrade_state`); err != nil {
		t.Fatalf("truncate autotrade_state: %v", err)
	}
	ch, cancel := topic.Subscribe()
	defer cancel()

	e.maybePublishFundingNeed("AUDJPY", "529:mt5", "277:mt5", "sizing insufficient")

	select {
	case got := <-ch:
		t.Fatalf("state error should fail-closed skip, got %+v", got)
	case <-time.After(100 * time.Millisecond):
		// 预期：无消息（fail-closed skip）。
	}
}
