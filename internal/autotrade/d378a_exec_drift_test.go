// d378a_exec_drift_test.go —— D-378a S5 锚2/3/4/5：exec_drift 闸+节拍+C1 组合保险丝（先红后绿）。
// 493 病例回放：quote 锚 −9.0、legs fill 构造 +5.3 → drift=14.3 → 首评估杀（不再饿到 basis-stop）。
package autotrade

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"arb/internal/combo"
	"arb/internal/store/pgstore"
	"arb/internal/testutil/storetest"
)

// d378aFptr float 指针（noise 列构造）。
func d378aFptr(v float64) *float64 { return &v }

// d378aOpenPos 按引擎退出链同源读取仓行（ListPositions→scanPositions，含 entry_noise_bps；
// GetPosition 单行查询不含该列——锚须同生产读取路径）。
func d378aOpenPos(t *testing.T, st *pgstore.Store, posID int64) *pgstore.Position {
	t.Helper()
	rows, err := st.ListPositions(context.Background(), "OPEN", 50, 0)
	if err != nil {
		t.Fatalf("list positions: %v", err)
	}
	for i := range rows {
		if rows[i].ID == posID {
			return &rows[i]
		}
	}
	t.Fatalf("position %d not found", posID)
	return nil
}

// d378aDriftPos 插入 XASSET 仓：quote 锚 −9.0、noise 可选，OPEN FILLED 双腿 fill 口径 +5.3
// （buy 100.053/sell 100.00 → fillBps=+5.3 → drift=+14.3）。
func d378aDriftPos(t *testing.T, st *pgstore.Store, noise *float64) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx,
		`TRUNCATE positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var posID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps, expected_entry_spread_bps, entry_noise_bps)
		 VALUES ('XASSET_CARRY:XAUUSD:a:b','XASSET_CARRY','OPEN',0,-9.0,$1) RETURNING id`, noise).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, venue, account, symbol, side, qty, price_expected, price_actual, status)
		 VALUES ($1,1,'OPEN','277842155:mt5','a','XAUUSD','BUY',1,100,100.053,'FILLED'),
		        ($1,2,'OPEN','binance:perp','b','PAXGUSDT','SELL',1,100,100.00,'FILLED')`, posID); err != nil {
		t.Fatalf("seed legs: %v", err)
	}
	return posID
}

func d378aEngine(st *pgstore.Store, driftBps float64) *Engine {
	return &Engine{store: st, cfg: Config{XAssetExecDriftBps: driftBps}}
}

// TestD378a_ExecDriftKill 锚2a：drift=14.3 > thresh 8 → 杀，reason 含 exec_drift（493 回放）。
func TestD378a_ExecDriftKill(t *testing.T) {
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	posID := d378aDriftPos(t, st, nil)
	p := d378aOpenPos(t, st, posID)
	should, reason := d378aEngine(st, 8.0).execDriftCheck(context.Background(), p)
	if !should {
		t.Fatalf("锚2a: drift=14.3 > 8 应杀（493 回放）")
	}
	if !strings.Contains(reason, "exec_drift") {
		t.Fatalf("锚2a: reason = %q, want 含 exec_drift", reason)
	}
}

// TestD378a_ExecDriftPass 锚2b：drift≈5 < 8 不杀（494/495 型放行）+ noise 抬阈生效。
func TestD378a_ExecDriftPass(t *testing.T) {
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	posID := d378aDriftPos(t, st, nil)
	// 漂移 ~5：fill=−4.0 vs 锚 −9.0（fillBps=(99.96−100)/99.96×1e4≈−4.0 → drift=+5.0）
	if _, err := st.Pool().Exec(ctx, `UPDATE position_legs SET price_actual = 99.96 WHERE side = 'BUY'`); err != nil {
		t.Fatalf("rewire fill: %v", err)
	}
	p := d378aOpenPos(t, st, posID)
	if should, _ := d378aEngine(st, 8.0).execDriftCheck(ctx, p); should {
		t.Fatal("锚2b: drift=5 < 8 不应杀（494/495 型）")
	}
	// noise 抬阈：drift=14.3、noise=20 → thresh=max(8,20)=20 → 不杀
	posID2 := d378aDriftPos(t, st, d378aFptr(20.0))
	p2 := d378aOpenPos(t, st, posID2)
	if should, _ := d378aEngine(st, 8.0).execDriftCheck(ctx, p2); should {
		t.Fatal("锚2b: noise=20 抬阈后 drift=14.3 不应杀")
	}
}

// TestExitKindFromReason_ExecDrift 锚2c：exit_kind='exec_drift' 映射+置于 carry/funding 分支前
// （reason 同时含 exec_drift 与 carry 字样时仍判 exec_drift）。
func TestExitKindFromReason_ExecDrift(t *testing.T) {
	if k := pgstore.ExitKindFromReason("exec_drift fill 5.30bps vs quote anchor -9.00bps"); k != "exec_drift" {
		t.Fatalf("锚2c: kind = %q, want exec_drift", k)
	}
	if k := pgstore.ExitKindFromReason("exec_drift carry sanity"); k != "exec_drift" {
		t.Fatalf("锚2c: 含 carry 字样仍应判 exec_drift（分支序锚）, got %q", k)
	}
}

// TestD378a_FallbackInterval 锚3：节拍配置化——cfg>0 生效、≤0 回退 10min 防呆。
func TestD378a_FallbackInterval(t *testing.T) {
	e := &Engine{cfg: Config{PositionFallbackInterval: 120 * time.Second}}
	if got := e.fallbackInterval(); got != 120*time.Second {
		t.Fatalf("锚3: fallback = %v, want 120s（ARB_POSITION_EVAL_SEC=120 注入）", got)
	}
	if got := (&Engine{cfg: Config{}}).fallbackInterval(); got != 10*time.Minute {
		t.Fatalf("锚3: 零值回退 = %v, want 10min（防呆）", got)
	}
}

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
// 2 行放行；window 外不计；limit=0 关闭；查询失败 fail-closed 由 Submit 分支承接（同 cooldown）。
func TestD378a_ComboLossGate_DB(t *testing.T) {
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
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

// TestD378a_LifecycleExecDrift 锚5：真库全链——quote 锚仓→exec_drift 杀→exit_kind 落列→
// ListCooledExits({stop,exec_drift}) 可见（gate C1 计数源闭环）。
func TestD378a_LifecycleExecDrift(t *testing.T) {
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	posID := d378aDriftPos(t, st, nil)
	p := d378aOpenPos(t, st, posID)
	should, reason := d378aEngine(st, 8.0).execDriftCheck(context.Background(), p)
	if !should {
		t.Fatal("锚5: exec_drift 应触发")
	}
	if err := st.UpdatePositionExitKind(context.Background(), posID, pgstore.ExitKindFromReason(reason)); err != nil {
		t.Fatalf("write exit_kind: %v", err)
	}
	// 平仓落态（ListCooledExits 只看 status='CLOSED'；模拟 logCloseTrigger 同轮伴生写）
	if _, err := st.Pool().Exec(context.Background(),
		`UPDATE positions SET status='CLOSED', closed_at=now() WHERE id=$1`, posID); err != nil {
		t.Fatalf("close: %v", err)
	}
	cooled, err := st.ListCooledExits(context.Background(), []string{"stop", "exec_drift"}, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("list cooled: %v", err)
	}
	found := false
	for _, ce := range cooled {
		if ce.ExitKind == "exec_drift" {
			found = true
		}
	}
	if !found {
		t.Fatalf("锚5: exec_drift 行未进冷却查询（C1 计数源断）: %+v", cooled)
	}
}
