// engine_position_perpfund_exit_test.go —— D-133 T1：PERP_FUNDING 退出判定纯函数单测（先红后绿）。
// perpFundingExitDecision 五用例：①floor 未满+breach→不平且清零 ②floor 满+单次→不平（计数 1）
// ③连续 N 次→平（reason 含 held/count）④读数回正→清零 ⑤缺 fact→不平不计数。
package autotrade

import (
	"strings"
	"testing"
	"time"
)

// 测试常量：minHold=8h、debounce=3、exitPct=0（与默认配置同）。
const (
	t1MinHold  = 8 * time.Hour
	t1Debounce = 3
	t1ExitPct  = 0.0
)

// ① floor 未满 + netFunding≤0 → 不平且计数清零。
// 持有 1h（< 8h floor），netFunding=-1.0（breach），curCount=2（已有计数）→ 不平 + 清零。
func TestPerpFundingExit_FloorNotMet_ClearsCount(t *testing.T) {
	shouldClose, newCount, reason := perpFundingExitDecision(true, 1*time.Hour, -1.0, t1ExitPct, t1MinHold, t1Debounce, 2)
	if shouldClose {
		t.Fatal("floor 未满: expected shouldClose=false, got true")
	}
	if newCount != 0 {
		t.Fatalf("floor 未满: expected newCount=0 (cleared), got %d", newCount)
	}
	if reason != "" {
		t.Fatalf("floor 未满: expected empty reason, got %q", reason)
	}
}

// ② floor 满 + 单次 breach → 不平（计数 1）。
// 持有 9h（≥ 8h floor），netFunding=-0.5（breach），curCount=0 → 不平 + 计数=1。
func TestPerpFundingExit_FloorMet_SingleBreach_NoClose(t *testing.T) {
	shouldClose, newCount, _ := perpFundingExitDecision(true, 9*time.Hour, -0.5, t1ExitPct, t1MinHold, t1Debounce, 0)
	if shouldClose {
		t.Fatal("单次 breach: expected shouldClose=false (count 1 < 3), got true")
	}
	if newCount != 1 {
		t.Fatalf("单次 breach: expected newCount=1, got %d", newCount)
	}
}

// ③ 连续 N 次 breach → 平（reason 含 held/count）。
// 持有 10h，netFunding=-0.3（breach），curCount=2 → 第 3 次 → 平 + 计数=3。
func TestPerpFundingExit_ConsecutiveNBreaches_Closes(t *testing.T) {
	shouldClose, newCount, reason := perpFundingExitDecision(true, 10*time.Hour, -0.3, t1ExitPct, t1MinHold, t1Debounce, 2)
	if !shouldClose {
		t.Fatal("连续 N 次: expected shouldClose=true (count 3 ≥ 3), got false")
	}
	if newCount != 3 {
		t.Fatalf("连续 N 次: expected newCount=3, got %d", newCount)
	}
	if !strings.Contains(reason, "held") || !strings.Contains(reason, "for 3 checks") {
		t.Fatalf("连续 N 次: reason 应含 held/count, got %q", reason)
	}
}

// ④ 读数回正 → 计数清零。
// 持有 9h（floor 满），netFunding=2.0（> exitPct 0，不 breach），curCount=2 → 不平 + 清零。
func TestPerpFundingExit_ReadingRecovers_ClearsCount(t *testing.T) {
	shouldClose, newCount, _ := perpFundingExitDecision(true, 9*time.Hour, 2.0, t1ExitPct, t1MinHold, t1Debounce, 2)
	if shouldClose {
		t.Fatal("读数回正: expected shouldClose=false, got true")
	}
	if newCount != 0 {
		t.Fatalf("读数回正: expected newCount=0 (cleared), got %d", newCount)
	}
}

// ⑤ 缺 fact → 不平不计数（fail-closed，D-105 语义不变）。
// factOK=false，curCount=2 → 不平 + 计数原样保留（不累计也不清零）。
func TestPerpFundingExit_MissingFact_NoDecisionNoCount(t *testing.T) {
	shouldClose, newCount, reason := perpFundingExitDecision(false, 10*time.Hour, 0, t1ExitPct, t1MinHold, t1Debounce, 2)
	if shouldClose {
		t.Fatal("缺 fact: expected shouldClose=false (fail-closed), got true")
	}
	if newCount != 2 {
		t.Fatalf("缺 fact: expected newCount=2 (unchanged), got %d", newCount)
	}
	if reason != "" {
		t.Fatalf("缺 fact: expected empty reason, got %q", reason)
	}
}
