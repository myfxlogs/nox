package autotrade

import (
	"testing"
	"time"
)

// TestD131_ExitEvalStarved 看门狗判据锚（D-131 验收）：
// kick 送达+评估超窗未消化 → 饥荒；安静窗（无新事件/事件已消化/无仓）→ 不饥荒。
// 生产误报形态「carry 持仓 quote 恒 false、funding emit 60s 节奏空窗」在此等价于
// 无新 kick——新判据天然不告警。
func TestD131_ExitEvalStarved(t *testing.T) {
	e := &Engine{}
	now := time.Now()
	iv := e.fallbackInterval()

	// 无 kick（安静窗）→ 不饥荒
	e.openCount.Store(1)
	e.lastEvalAt.Store(now.Add(-24 * time.Hour).UnixNano())
	if _, _, _, s := e.exitEvalStarved(); s {
		t.Fatal("no kick → must not starve（旧判据误报形态）")
	}
	// kick 送达且评估已消化（lastEval 在 kick 之后）→ 不饥荒
	e.lastKickSent.Store(now.Add(-time.Minute).UnixNano())
	e.lastEvalAt.Store(now.Add(-time.Second).UnixNano())
	if _, _, _, s := e.exitEvalStarved(); s {
		t.Fatal("kick already consumed → must not starve")
	}
	// kick 送达但超窗未消化（loop 卡死/chan 堵）→ 饥荒
	e.lastKickSent.Store(now.UnixNano())
	e.lastEvalAt.Store(now.Add(-2 * iv).UnixNano())
	open, _, _, s := e.exitEvalStarved()
	if !s || open != 1 {
		t.Fatalf("kick delivered+loop stalled → want starved, got open=%d starved=%v", open, s)
	}
	// 无仓 → 不饥荒（链断无仓也无需告警）
	e.openCount.Store(0)
	if _, _, _, s := e.exitEvalStarved(); s {
		t.Fatal("open=0 → must not starve")
	}
}
