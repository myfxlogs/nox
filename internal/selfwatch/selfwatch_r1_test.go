// selfwatch_r1_test.go —— D-033 R1 锚：组合域无行不就绪（D-061 后=目标组合纸面行存活行）、
// 就绪推送仅 ❌→✅ 转换一次（app_config 态 + InsertAlertDedup 7d 兜底）。
package selfwatch

import (
	"context"
	"strings"
	"testing"

	"arb/internal/scan"
)

type fakeNotify struct{ sends []string }

func (n *fakeNotify) Enabled() bool { return true }
func (n *fakeNotify) SendText(_ context.Context, chatID, text string) error {
	n.sends = append(n.sends, chatID+"|"+text)
	return nil
}

// R1①：全结构 0 active 行 → 组合证据「无窗内行」❌ → Stage 1 不就绪
// （即便 D-028 判据已放至仅 stall=0、窗内净额组合口径 0≥0 仍不误报就绪）。
func TestD033_Stage1NoActive(t *testing.T) {
	st := greenStore()
	st.activeRows = nil // D-061：组合域无 active 行 → 存活行 ❌ → 不就绪
	rep, err := BuildDaily(context.Background(), testDeps(st,
		&scan.PaperWindowEval{WindowNet: 0, ActiveN: 0, ExitedN: 0}, tNow))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Stage1OK || !strings.Contains(rep.Body, "❌ 目标组合纸面行存活") {
		t.Fatalf("0-active should not be ready:\n%s", rep.Body)
	}
	// 组合域无行 → 组合净额 0≥0 仍 ✅（闸点在组合证据行，非净额行）
	if !strings.Contains(rep.Body, "✅ 窗内净额非负") {
		t.Fatalf("combo net 0 should be non-negative:\n%s", rep.Body)
	}
}

// R1②：就绪推送仅 ❌→✅ 转换一次（app_config 态跨调用存活），回绿再发。
func TestD033_Stage1ReadyTransitionOnce(t *testing.T) {
	st := greenStore()
	notify := &fakeNotify{}
	d := testDeps(st, goodEval(), tNow)
	d.Notify = notify
	d.ChatID = func(context.Context) (string, error) { return "oc_x", nil }

	emitStage1Ready(context.Background(), d, true) // ❌→✅：发
	emitStage1Ready(context.Background(), d, true) // ✅→✅：不发
	if len(st.dedupCalls) != 1 || !strings.Contains(st.dedupCalls[0], "stage1_ready") ||
		!strings.HasSuffix(st.dedupCalls[0], "|168h0m0s") {
		t.Fatalf("dedup calls: %v", st.dedupCalls)
	}
	if len(notify.sends) != 1 || !strings.HasPrefix(notify.sends[0], "oc_x|") {
		t.Fatalf("push should fire once: %v", notify.sends)
	}
	emitStage1Ready(context.Background(), d, false) // ✅→❌：回落写 0
	emitStage1Ready(context.Background(), d, true)  // ❌→✅：再发
	if len(st.dedupCalls) != 2 || len(notify.sends) != 2 {
		t.Fatalf("re-transition should fire again: %v %v", st.dedupCalls, notify.sends)
	}
}
