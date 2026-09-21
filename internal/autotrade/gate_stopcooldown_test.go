// gate_stopcooldown_test.go —— D-372 S3：gate 统一止损重进冷却锚测试（先红后绿）。
// 全链锚走真库 full-Submit（复用 setupLiveGateStore：demo 腿过 LIVE gate；CROSS_VENUE 免
// comboAllow 干扰），覆盖 §7-S3 八锚 + <=0 逃生阀 + gateDecision 纯函数锚。
// fail-closed 锚以 exit_kind 列改名注入查询错误——DailyRealizedPnl/ListActivePositions
// 均不引用该列，精确打到 ListCooledExits；defer 改回复原（包内测试串行，共享库无并发暴露）。
package autotrade

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/combo"
	"arb/internal/store/pgstore"
)

// ckStopCooldown 测试组合键（vA/vB demo 腿，AUDJPY）。
func ckStopCooldown() string { return combo.Key("CROSS_VENUE", "AUDJPY", "vA:mt5", "vB:mt5") }

// seedCooledExit 落一行 CLOSED 持仓（opportunity_id/exit_kind/closed_at=冷却检查取用列）。
func seedCooledExit(t *testing.T, s *pgstore.Store, oppID, kind string, closedAt time.Time) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(),
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps, opened_at, closed_at, exit_kind)
		 VALUES ($1, 'CROSS_VENUE', 'CLOSED', 0, now(), $2, $3)`, oppID, closedAt, kind); err != nil {
		t.Fatalf("seed closed position: %v", err)
	}
}

// newCooldownGate 真库 store（RUNNING + demo 腿）+ 开冷却的 gate。
func newCooldownGate(t *testing.T, cooldown time.Duration) (*pgstore.Store, *Gate) {
	t.Helper()
	s := setupLiveGateStore(t, "vA:mt5", "vB:mt5", false, false)
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5, StopReentryCooldown: cooldown}, nil)
	return s, g
}

// cooldownSubmit 构造过前置检查的 intent 并 Submit（strategy/dedupKey 可换，锚 8 用）。
func cooldownSubmit(t *testing.T, g *Gate, strategy, dedupKey string) GateResult {
	t.Helper()
	intent := TradeIntent{
		Strategy: strategy, Canonical: "AUDJPY",
		VenueBuy: "vA:mt5", VenueSell: "vB:mt5", Qty: 0.01,
		DedupKey: dedupKey,
		Execute: func(ctx context.Context) (string, string, int64) {
			return "EXECUTED", "", 1
		},
	}
	return g.Submit(context.Background(), &intent)
}

// 锚 1：同 ComboKey exit_kind=stop 10min 前 → REJECTED（stop→秒级重进毒环主锚）。
func TestGateStopCooldown_Stop10min_Rejected(t *testing.T) {
	s, g := newCooldownGate(t, time.Hour)
	seedCooledExit(t, s, ckStopCooldown(), "stop", time.Now().Add(-10*time.Minute))
	res := cooldownSubmit(t, g, "CROSS_VENUE", ckStopCooldown())
	if res.Status != "REJECTED" || !strings.Contains(res.Reason, "combo reentry cooldown") {
		t.Fatalf("锚1: want REJECTED(cooldown), got %s (%s)", res.Status, res.Reason)
	}
	if !strings.Contains(res.Reason, "stop") {
		t.Fatalf("锚1: reason 缺 exit_kind 审计信息: %q", res.Reason)
	}
}

// 锚 2：stop 70min 前（窗口外）→ 放行。
func TestGateStopCooldown_Stop70min_Pass(t *testing.T) {
	s, g := newCooldownGate(t, time.Hour)
	seedCooledExit(t, s, ckStopCooldown(), "stop", time.Now().Add(-70*time.Minute))
	if res := cooldownSubmit(t, g, "CROSS_VENUE", ckStopCooldown()); res.Status != "EXECUTED" {
		t.Fatalf("锚2: want EXECUTED, got %s (%s)", res.Status, res.Reason)
	}
}

// 锚 3：harvest 10min 前 → 放行（收割=价差收敛到位，重开=合法新信号）。
func TestGateStopCooldown_Harvest10min_Pass(t *testing.T) {
	s, g := newCooldownGate(t, time.Hour)
	seedCooledExit(t, s, ckStopCooldown(), "harvest", time.Now().Add(-10*time.Minute))
	if res := cooldownSubmit(t, g, "CROSS_VENUE", ckStopCooldown()); res.Status != "EXECUTED" {
		t.Fatalf("锚3: want EXECUTED, got %s (%s)", res.Status, res.Reason)
	}
}

// 锚 4：manual 10min 前 → REJECTED（人工平仓后被机器人开回=违背操作意图）。
func TestGateStopCooldown_Manual10min_Rejected(t *testing.T) {
	s, g := newCooldownGate(t, time.Hour)
	seedCooledExit(t, s, ckStopCooldown(), "manual", time.Now().Add(-10*time.Minute))
	if res := cooldownSubmit(t, g, "CROSS_VENUE", ckStopCooldown()); res.Status != "REJECTED" || !strings.Contains(res.Reason, "manual") {
		t.Fatalf("锚4: want REJECTED(manual cooldown), got %s (%s)", res.Status, res.Reason)
	}
}

// 锚 5：不同 ComboKey 的 stop → 放行（冷却按组合键隔离）。
func TestGateStopCooldown_OtherComboStop_Pass(t *testing.T) {
	s, g := newCooldownGate(t, time.Hour)
	other := combo.Key("CROSS_VENUE", "XAUUSD", "vA:mt5", "vB:mt5")
	seedCooledExit(t, s, other, "stop", time.Now().Add(-10*time.Minute))
	if res := cooldownSubmit(t, g, "CROSS_VENUE", ckStopCooldown()); res.Status != "EXECUTED" {
		t.Fatalf("锚5: want EXECUTED, got %s (%s)", res.Status, res.Reason)
	}
}

// 锚 6：exit_kind 空串 → 放行（SQL ANY 天然排除空串/NULL）。
func TestGateStopCooldown_EmptyExitKind_Pass(t *testing.T) {
	s, g := newCooldownGate(t, time.Hour)
	seedCooledExit(t, s, ckStopCooldown(), "", time.Now().Add(-10*time.Minute))
	if res := cooldownSubmit(t, g, "CROSS_VENUE", ckStopCooldown()); res.Status != "EXECUTED" {
		t.Fatalf("锚6: want EXECUTED, got %s (%s)", res.Status, res.Reason)
	}
}

// 锚 7：查询错误 → REJECTED（fail-closed）。exit_kind 列改名 → 仅 ListCooledExits 报错，
// 前置检查（state/strategy/pnl/active）不受影响 → 精确打到冷却查询失败分支。
func TestGateStopCooldown_QueryError_FailClosed(t *testing.T) {
	s, g := newCooldownGate(t, time.Hour)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `ALTER TABLE positions RENAME COLUMN exit_kind TO exit_kind_d372_probe`); err != nil {
		t.Fatalf("inject query error: %v", err)
	}
	defer func() {
		if _, err := s.Pool().Exec(ctx, `ALTER TABLE positions RENAME COLUMN exit_kind_d372_probe TO exit_kind`); err != nil {
			t.Errorf("restore exit_kind column: %v", err)
		}
	}()
	res := cooldownSubmit(t, g, "CROSS_VENUE", ckStopCooldown())
	if res.Status != "REJECTED" || !strings.Contains(res.Reason, "cooled exits check failed") {
		t.Fatalf("锚7: want REJECTED(fail-closed), got %s (%s)", res.Status, res.Reason)
	}
}

// 锚 8：SWAPARB intent 同 ComboKey stop → 同样 REJECTED（统一覆盖锚：冷却检查在 gateDecision 层）。
// cfg 开 SwapArbAutoAllow 让 comboAllow 栈放行——gate.go 中 SWAPARB comboAllow 块先于
// verdict 拒绝处理，无 allowlist 时会以 combo 理由先拒，冷却 reason 浮不出。
func TestGateStopCooldown_SwapArbSameCombo_Rejected(t *testing.T) {
	s, g := newCooldownGate(t, time.Hour)
	g.cfg.SwapArbAutoAllow = true // comboAllow 放行 → 冷却拒绝理由可见
	sk := combo.Key("SWAPARB", "GOLD", "vA:mt5", "vB:mt5")
	seedCooledExit(t, s, sk, "stop", time.Now().Add(-10*time.Minute))
	if res := cooldownSubmit(t, g, "SWAPARB", sk); res.Status != "REJECTED" || !strings.Contains(res.Reason, "combo reentry cooldown") {
		t.Fatalf("锚8: want REJECTED(cooldown), got %s (%s)", res.Status, res.Reason)
	}
}

// 逃生阀：StopReentryCooldown<=0 → 检查关闭（窗口内 stop 也放行）。
func TestGateStopCooldown_ZeroCooldownDisabled(t *testing.T) {
	s, g := newCooldownGate(t, 0)
	seedCooledExit(t, s, ckStopCooldown(), "stop", time.Now().Add(-10*time.Minute))
	if res := cooldownSubmit(t, g, "CROSS_VENUE", ckStopCooldown()); res.Status != "EXECUTED" {
		t.Fatalf("逃生阀: cooldown=0 want EXECUTED, got %s (%s)", res.Status, res.Reason)
	}
}

// gateDecision 纯函数锚：StopCooldownHit → 拒绝且 reason 带 exit_kind 与距今时长。
func TestGateDecision_StopCooldownHit_Verdict(t *testing.T) {
	v := gateDecision(GateInput{
		State: StateRunning, MaxDailyLoss: 100, MaxPositions: 5,
		StopCooldownHit: true, StopCooldownKind: "stop", StopCooldownAgo: 10 * time.Minute,
	})
	if v.Pass {
		t.Fatal("纯函数锚: want reject on stop cooldown hit")
	}
	if !strings.Contains(v.Reason, "combo reentry cooldown") || !strings.Contains(v.Reason, "stop") || !strings.Contains(v.Reason, "ago") {
		t.Fatalf("纯函数锚: reason 缺审计字段: %q", v.Reason)
	}
}
