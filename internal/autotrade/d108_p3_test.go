// d108_p3_test.go —— D-108 P3 C1：额度门 notional 判定单测（先红后绿）。
// V1 GateInput 加 NotionalUSD/MaxNotional/SkipNotional 三字段，判定插「持仓数」后「幂等」前。
// V3 表驱动：超额拒绝 / SkipNotional 放行 / MaxNotional≤0 禁用 / 五道顺序回归（含 notional 插入位置）。
package autotrade

import (
	"strings"
	"testing"
)

// TestD108P3_GateDecision_Notional 表驱动覆盖六道判定 + notional 三语义。
func TestD108P3_GateDecision_Notional(t *testing.T) {
	tests := []struct {
		name       string
		in         GateInput
		wantPass   bool
		wantReason string // 空则不检查
	}{
		// --- 既有五道顺序回归（notional 字段零值，不影响） ---
		{"state halt", GateInput{State: StateDailyLossHalt, MaxDailyLoss: 100, MaxPositions: 5}, false, "system state"},
		{"disabled", GateInput{State: StateRunning, Disabled: true, MaxDailyLoss: 100, MaxPositions: 5}, false, "disabled"},
		{"daily loss", GateInput{State: StateRunning, DailyPnl: -150, MaxDailyLoss: 100, MaxPositions: 5}, false, "daily loss"},
		{"max positions", GateInput{State: StateRunning, OpenCount: 5, MaxPositions: 5, MaxDailyLoss: 100}, false, "max positions"},
		{"dedup hit", GateInput{State: StateRunning, DedupHit: true, MaxDailyLoss: 100, MaxPositions: 5}, false, "duplicate dedup"},
		{"all pass", GateInput{State: StateRunning, Strategy: "MEANREVERT", MaxDailyLoss: 100, MaxPositions: 5, NotionalUSD: 1000, MaxNotional: 5000}, true, ""},

		// --- V1 notional 三语义 ---
		// 超额拒绝（NotionalUSD > MaxNotional，数值断言）
		{"notional over cap", GateInput{State: StateRunning, Strategy: "MEANREVERT", MaxDailyLoss: 100, MaxPositions: 5, NotionalUSD: 6000, MaxNotional: 5000}, false, "notional $6000.00 > cap $5000.00"},
		// SkipNotional 放行（SWAPARB 豁免：NotionalUSD 超 cap 但 SkipNotional=true）
		{"swaparb skip notional", GateInput{State: StateRunning, Strategy: "SWAPARB", MaxDailyLoss: 100, MaxPositions: 5, NotionalUSD: 999999, MaxNotional: 5000, SkipNotional: true}, true, ""},
		// MaxNotional≤0 禁用（NotionalUSD 超 0 但 cap=0 不启用）
		{"notional cap disabled", GateInput{State: StateRunning, Strategy: "MEANREVERT", MaxDailyLoss: 100, MaxPositions: 5, NotionalUSD: 999999, MaxNotional: 0}, true, ""},
		// MaxNotional<0 禁用（负值也不启用）
		{"notional cap negative", GateInput{State: StateRunning, Strategy: "MEANREVERT", MaxDailyLoss: 100, MaxPositions: 5, NotionalUSD: 999999, MaxNotional: -1}, true, ""},

		// --- 顺序回归：notional 在「持仓数」后「幂等」前 ---
		// max positions 优先于 notional（两者都触发 → 返回 max positions 原因）
		{"order: max positions before notional", GateInput{State: StateRunning, OpenCount: 5, MaxPositions: 5, MaxDailyLoss: 100, NotionalUSD: 999999, MaxNotional: 5000}, false, "max positions"},
		// notional 优先于 dedup（两者都触发 → 返回 notional 原因）
		{"order: notional before dedup", GateInput{State: StateRunning, DedupHit: true, MaxDailyLoss: 100, MaxPositions: 5, NotionalUSD: 999999, MaxNotional: 5000}, false, "notional"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := gateDecision(tt.in)
			if v.Pass != tt.wantPass {
				t.Fatalf("pass=%v want %v (reason=%q)", v.Pass, tt.wantPass, v.Reason)
			}
			if tt.wantReason != "" && !strings.Contains(v.Reason, tt.wantReason) {
				t.Errorf("reason=%q want contains %q", v.Reason, tt.wantReason)
			}
		})
	}
}
