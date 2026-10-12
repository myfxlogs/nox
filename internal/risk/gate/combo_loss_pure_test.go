// combo_loss_pure_test.go —— D-378a 锚4a：gateDecision ComboLossHit 纯函数锚
// （d378a_exec_drift_test 拆出，D-212 拆搬）。
package gate

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"nox/internal/platform/combo"
	"nox/internal/platform/testutil/storetest"
	"time"
)

// TestD378a_ComboLossPure 锚4a：gateDecision ComboLossHit → 拒绝且 reason 带计数。
func TestD378a_ComboLossPure(t *testing.T) {
	v := gateDecision(GateInput{
		State: StateRunning, MaxDailyLoss: 100, MaxPositions: 5,
		ComboLossHit: true, ComboLossCount: 3,
	})
	if v.Pass {
		t.Fatal("锚4a: ComboLossHit 应拒绝")
	}
	if !strings.Contains(v.Reason, "combo loss limit") || !strings.Contains(v.Reason, "3") {
		t.Fatalf("锚4a: reason = %q, want combo loss limit + 计数", v.Reason)
	}
	// 顺序锚：StopCooldownHit 优先于 ComboLossHit（cooldown 先判）
	v2 := gateDecision(GateInput{State: StateRunning, MaxDailyLoss: 100, MaxPositions: 5,
		StopCooldownHit: true, ComboLossHit: true, ComboLossCount: 3})
	if !strings.Contains(v2.Reason, "combo reentry cooldown") {
		t.Fatalf("锚4a: cooldown 应先于 combo loss, got %q", v2.Reason)
	}
}

// TestD378a_ComboLossGate_DB 锚4b：真库 3 行 stop/exec_drift 同 ComboKey in 24h → Hit=3；

func TestD378a_ComboLossGate_DB(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx,
		`TRUNCATE positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ck := combo.Key("SWAPARB", "AUDJPY", "a:mt5", "b:mt5")
	seedClosedUnder := func(key string, i int, kind string, hoursAgo int) {
		t.Helper()
		var id int64
		if err := st.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ($1,'SWAPARB','CLOSED',0) RETURNING id`,
			fmt.Sprintf("%s:%d", key, i)).Scan(&id); err != nil {
			t.Fatalf("seed closed: %v", err)
		}
		if _, err := st.Pool().Exec(ctx,
			`UPDATE positions SET exit_kind=$2, closed_at=now()-make_interval(hours=>$3) WHERE id=$1`,
			id, kind, hoursAgo); err != nil {
			t.Fatalf("seed exit: %v", err)
		}
	}
	seedClosedUnder(ck, 1, "stop", 2)
	seedClosedUnder(ck, 2, "exec_drift", 3)
	seedClosedUnder(ck, 3, "stop", 5)
	seedClosedUnder(ck, 4, "stop", 30) // window 外（>24h）不计
	// 他组合行不串扰（异 venue 组合键）
	seedClosedUnder(combo.Key("SWAPARB", "AUDJPY", "a:mt5", "c:mt5"), 9, "stop", 2)

	g := NewGate(st, Config{ComboLossLimit: 3, ComboLossWindow: 24 * time.Hour}, nil)
	hit, count, err := g.comboLossCheck(ctx, ck)
	if err != nil || !hit || count != 3 {
		t.Fatalf("锚4b: hit=%v count=%d err=%v, want true/3（window 外与异组合行不计）", hit, count, err)
	}
	// 2 行（limit=3）→ 放行
	if _, err := st.Pool().Exec(ctx, `DELETE FROM positions WHERE opportunity_id LIKE '%:1' OR opportunity_id LIKE '%:2'`); err != nil {
		t.Fatalf("trim: %v", err)
	}
	if hit, count, _ := g.comboLossCheck(ctx, ck); hit || count != 1 {
		t.Fatalf("锚4b: 余 1 行 hit=%v count=%d, want false/1", hit, count)
	}
	// limit=0 关闭（逃生阀）
	g0 := NewGate(st, Config{ComboLossWindow: 24 * time.Hour}, nil)
	if hit, _, _ := g0.comboLossCheck(ctx, ck); hit {
		t.Fatal("锚4b: limit=0 应关闭检查")
	}
}

// A7（C1 集成）：同组合 24h 内 3 次 disaster 平仓 → comboLossCheck 命中（kind 集含 disaster）。
func TestD378b_A7_C1DisasterCounted(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx,
		`TRUNCATE positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ck := combo.Key("SWAPARB", "AUDJPY", "a:mt5", "b:mt5")
	for i := 1; i <= 3; i++ {
		var id int64
		if err := st.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ($1,'SWAPARB','CLOSED',0) RETURNING id`,
			fmt.Sprintf("%s:%d", ck, i)).Scan(&id); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := st.Pool().Exec(ctx,
			`UPDATE positions SET exit_kind='disaster', closed_at=now()-make_interval(hours=>$2) WHERE id=$1`, id, i); err != nil {
			t.Fatalf("seed kind: %v", err)
		}
	}
	g := NewGate(st, Config{ComboLossLimit: 3, ComboLossWindow: 24 * time.Hour}, nil)
	hit, count, err := g.comboLossCheck(ctx, ck)
	if err != nil || !hit || count != 3 {
		t.Fatalf("A7: hit=%v count=%d err=%v, want true/3（kind 集含 disaster）", hit, count, err)
	}
}
