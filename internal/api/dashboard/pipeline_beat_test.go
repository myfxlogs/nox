// pipeline_beat_test.go —— S3′-c 真心跳返修锚：静默期不误报 + 真超龄才报。
package dashboard

import (
	"testing"
	"time"
)

func TestBeatStaleness(t *testing.T) {
	now := time.Now()
	// ①静默期不报警：beat 段 last_event 新鲜 → working 不动（无 opp 帧
	// 不再假断流——beat 源=watch 环非事件帧）。
	fresh := &stageRow{EvidenceKind: "beat", State: "working", LastEventUnix: now.Add(-1 * time.Minute).Unix()}
	// ②真超龄才报：beat 段 last_event 超 15min 窗 → waiting 断流嫌疑。
	stale := &stageRow{EvidenceKind: "beat", State: "working", LastEventUnix: now.Add(-20 * time.Minute).Unix()}
	// ③事件驱动段超龄不动（event 型零帧=如实 idle/working 非断流）。
	ev := &stageRow{EvidenceKind: "event", State: "working", LastEventUnix: now.Add(-20 * time.Minute).Unix()}
	// ④从未拍不翻 error（0=未记账如实）。
	never := &stageRow{EvidenceKind: "beat", State: "idle", LastEventUnix: 0}
	applyBeatStaleness([]*stageRow{fresh, stale, ev, never}, now)
	if fresh.State != "working" {
		t.Fatalf("fresh beat 应保 working: %s", fresh.State)
	}
	if stale.State != "waiting" || stale.WaitReason == "" {
		t.Fatalf("stale beat 应 waiting 带因: %s %q", stale.State, stale.WaitReason)
	}
	if ev.State != "working" {
		t.Fatalf("event 段超龄不应动: %s", ev.State)
	}
	if never.State != "idle" {
		t.Fatalf("从未拍应保 idle: %s", never.State)
	}
}
