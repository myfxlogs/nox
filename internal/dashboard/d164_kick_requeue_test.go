// d164_kick_requeue_test.go —— D-164：no-match kick 有界延迟重排锚。
// attach 竞态窗（Connect 内帧先达、querier 后注册）首 miss → 一次延迟重排；
// 再 miss → warn+丢不无限重排；attach 窗内重排命中 → 标记清除照常有额度。
package dashboard

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/collect/exchange"
	"arb/internal/exec"

	v1 "arb/internal/gen/dashboardv1"
)

func d164Mon(t *testing.T, bm *BrokerManager) *BrokerPositionMonitor {
	t.Helper()
	return NewBrokerPositionMonitor(bm, bus.New(), time.Minute,
		bus.NewTopic[*v1.BrokerPositionEvent](),
		NewServerTZCache(time.Minute, func(context.Context, string) (float64, error) { return 0, errors.New("n/a") }),
		exchange.NewFundingIntervalCache(), nil)
}

func d164Delay(t *testing.T, d time.Duration) {
	t.Helper()
	old := bpKickRetryDelay
	bpKickRetryDelay = d
	t.Cleanup(func() { bpKickRetryDelay = old })
}

// 首 miss → 延迟重排入 pendingKick（drain 同径再查）；再 miss → warn 丢、不再排。
func TestD164_NoMatchKick_RequeueOnce(t *testing.T) {
	d164Delay(t, 30*time.Millisecond)
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute) // 零 querier
	m := d164Mon(t, bm)
	ctx := context.Background()

	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)

	m.reconcileVenueKeys(ctx, "3001157:mt5") // attach 窗 miss——不 warn，重排一次
	if strings.Contains(logs.String(), "no matching querier") {
		t.Fatal("首 miss 不应 warn（attach 竞态窗应静默重排）")
	}
	time.Sleep(80 * time.Millisecond) // 重排延迟到——pendingKick 应回填
	got := m.drainKicks()
	if len(got) != 1 || got[0] != "3001157:mt5" {
		t.Fatalf("重排未入 pendingKick: %v", got)
	}
	m.reconcileVenueKeys(ctx, "3001157:mt5") // 重排后仍 miss → warn+丢
	if !strings.Contains(logs.String(), "after retry (dropped)") {
		t.Fatalf("再 miss 未 warn: %q", logs.String())
	}
	time.Sleep(80 * time.Millisecond) // 确认无二次重排（有界）
	if got := m.drainKicks(); len(got) != 0 {
		t.Fatalf("二次重排越界: %v", got)
	}
}

// attach 竞态自愈：首 miss 重排 → 窗内 querier 注册 → 重排命中 → 标记清除（新 miss 仍有额度）。
func TestD164_Requeue_HitsAfterAttach(t *testing.T) {
	d164Delay(t, 30*time.Millisecond)
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	m := d164Mon(t, bm)
	ctx := context.Background()

	m.reconcileVenueKeys(ctx, "3001157:mt5")
	bm.AddStaticQuerier("3001157:mt5", &fakeVenueQuerier{key: "3001157:mt5"}) // attach 完成
	time.Sleep(80 * time.Millisecond)
	got := m.drainKicks()
	if len(got) != 1 {
		t.Fatalf("重排未回填: %v", got)
	}
	m.reconcileVenueKeys(ctx, got[0]) // 命中 → 标记清除
	m.mu.Lock()
	_, marked := m.kickRetried["3001157:mt5"]
	m.mu.Unlock()
	if marked {
		t.Fatal("命中后重排标记未清——后续 attach 分离将失去重排额度")
	}
}
