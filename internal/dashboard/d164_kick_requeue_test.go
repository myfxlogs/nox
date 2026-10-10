// d164_kick_requeue_test.go —— D-164 R5：no-match kick attach-pending 感知重排锚。
// miss+attaching → 延迟重排（可多次至 attach 完）；miss+非 attaching → 立即 warn 丢。
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

func d164LogCapture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })
	return &logs
}

// 非 attaching 的 miss=真未知 venue → 立即 warn+丢，且不再重排。
func TestD164R5_NoMatchNonAttaching_WarnDrop(t *testing.T) {
	d164Delay(t, 30*time.Millisecond)
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute) // 零 querier 零 attaching
	m := d164Mon(t, bm)
	ctx := context.Background()
	logs := d164LogCapture(t)

	m.reconcileVenueKeys(ctx, "3001157:mt5")
	if !strings.Contains(logs.String(), "no matching querier (dropped)") {
		t.Fatalf("非 attaching miss 未立即 warn: %q", logs.String())
	}
	time.Sleep(80 * time.Millisecond) // 确认无重排
	if got := m.drainKicks(); len(got) != 0 {
		t.Fatalf("非 attaching miss 不应重排: %v", got)
	}
}

// attaching 态 miss → 重排，可多次至 attach 完；attach 完注册 querier → 重排命中。
func TestD164R5_Attaching_RequeueUntilAttach(t *testing.T) {
	d164Delay(t, 30*time.Millisecond)
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	m := d164Mon(t, bm)
	ctx := context.Background()
	logs := d164LogCapture(t)

	bm.setAttaching("3001157:mt5") // startMT5 入口置位形态
	defer bm.clearAttaching("3001157:mt5")

	m.reconcileVenueKeys(ctx, "3001157:mt5") // attach 窗 miss——不 warn，重排
	if strings.Contains(logs.String(), "no matching querier") {
		t.Fatal("attaching miss 不应 warn（竞态窗静默重排）")
	}
	time.Sleep(80 * time.Millisecond)
	got := m.drainKicks()
	if len(got) != 1 || got[0] != "3001157:mt5" {
		t.Fatalf("重排未入 pendingKick: %v", got)
	}
	m.reconcileVenueKeys(ctx, got[0]) // 仍 attaching 再 miss → 再重排（可多次）
	time.Sleep(80 * time.Millisecond)
	got = m.drainKicks()
	if len(got) != 1 {
		t.Fatalf("attaching 期应继续重排: %v", got)
	}
	bm.AddStaticQuerier("3001157:mt5", &fakeVenueQuerier{key: "3001157:mt5"}) // attach 完成
	m.reconcileVenueKeys(ctx, got[0])                                         // 在途重排命中 → 无 warn
	if strings.Contains(logs.String(), "dropped") {
		t.Fatalf("命中后不应 warn: %q", logs.String())
	}
	time.Sleep(80 * time.Millisecond) // 后续不再产生新重排
	if got := m.drainKicks(); len(got) != 0 {
		t.Fatalf("命中后不应再重排: %v", got)
	}
}

// attaching 终止（含 Connect 失败的清位径）→ 下一次 miss 立即 warn 丢。
func TestD164R5_AttachAborted_WarnDrop(t *testing.T) {
	d164Delay(t, 30*time.Millisecond)
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	m := d164Mon(t, bm)
	ctx := context.Background()
	logs := d164LogCapture(t)

	bm.setAttaching("3001157:mt5")
	m.reconcileVenueKeys(ctx, "3001157:mt5") // attaching → 重排一次
	time.Sleep(80 * time.Millisecond)
	m.drainKicks()
	bm.clearAttaching("3001157:mt5") // attach 中止（Connect 失败径同此清位）
	m.reconcileVenueKeys(ctx, "3001157:mt5")
	if !strings.Contains(logs.String(), "no matching querier (dropped)") {
		t.Fatalf("清位后 miss 未 warn: %q", logs.String())
	}
}

// isAttaching 键形：bare 名 kick 命中 joined attaching 键（同 queriers() BareOf 匹配规）。
func TestD164R5_IsAttaching_KeyForms(t *testing.T) {
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	bm.setAttaching("3001157:mt5")
	defer bm.clearAttaching("3001157:mt5")
	for _, k := range []string{"3001157:mt5", "3001157"} {
		if !bm.isAttaching(k) {
			t.Fatalf("isAttaching(%q)=false", k)
		}
	}
	if bm.isAttaching("3001158") || bm.isAttaching("3001157:mt4") {
		t.Fatal("异 venue/异类型误中 attaching")
	}
}
