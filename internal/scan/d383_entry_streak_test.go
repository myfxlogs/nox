// d383_entry_streak_test.go —— D-383 Phase C：paper spot_carry 进场 N=3 轮持续闸。
// 锚 C1-C6（SSOT §6.3）：瞬时尖峰不开/连续 3 轮第 3 轮开（C1）、中断清零重计（C2）、
// 退出 low_streak 不变（C3）、重启清零保守重计（C4）、键级 venue 隔离（C5）、
// uni 消失回归从 0 重计（C6）。
package scan

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// eligibleStreak 预灌满轮数的计数器：供既有 D-350/D-354 锚专注闸外机制
// （开仓/累积/退出/cap/排序），持续闸行为归本文件 C 锚专测。
func eligibleStreak(keys ...string) *EntryStreak {
	es := NewEntryStreak()
	for i := 0; i < PaperEntryStreakRounds; i++ {
		es.Step(keys, PaperEntryStreakRounds)
	}
	return es
}

// stepOpen 步进一轮并断言无错，返回 opened 清单。
func stepOpen(t *testing.T, s *pgstore.Store, es *EntryStreak, now time.Time) []string {
	t.Helper()
	opened, _, err := StepPaperSpotCarry(context.Background(), s, es, now)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	return opened
}

// TestD383C1_EntrySpikeNeedsThreeRounds C1：瞬时尖峰单轮出现→不开仓；连续 3 轮达标→第 3 轮开仓。
func TestD383C1_EntrySpikeNeedsThreeRounds(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedUniVol(t, s, "binance", "GUSDT", "TRADING", "T3", 72*time.Hour, 92, 100e6)
	seedUniVol(t, s, "binance", "SPIKEUSDT", "TRADING", "T3", 72*time.Hour, 250, 100e6)
	es := NewEntryStreak()

	// r1：双候选全条件达标——streak=1，均不开（红=闸未生效首轮即开）
	if opened := stepOpen(t, s, es, now); len(opened) != 0 {
		t.Fatalf("r1 opened=%v, want 空（瞬时尖峰第 1 轮不得开仓）", opened)
	}
	// r2：尖峰回落出 band（250→5），GUSDT 仍达标——SPIKEUSDT 清零
	if _, err := s.Pool().Exec(ctx, `UPDATE fin_cex_universe SET last_funding_ann = 5 WHERE symbol = 'SPIKEUSDT'`); err != nil {
		t.Fatalf("drop spike: %v", err)
	}
	if opened := stepOpen(t, s, es, now.Add(time.Hour)); len(opened) != 0 {
		t.Fatalf("r2 opened=%v, want 空（连续未满 3 轮）", opened)
	}
	// r3：GUSDT 连续第 3 轮 → 只开 GUSDT；SPIKEUSDT 单轮尖峰永不开
	opened := stepOpen(t, s, es, now.Add(2*time.Hour))
	if len(opened) != 1 || opened[0] != "GUSDT" {
		t.Fatalf("r3 opened=%v, want [GUSDT]（连续 3 轮第 3 轮放行）", opened)
	}
	rows, err := s.ListActivePaperSpotCarry(ctx)
	if err != nil || len(rows) != 1 || rows[0].Symbol != "GUSDT" {
		t.Fatalf("active rows=%v err=%v, want 仅 GUSDT（尖峰候选未开）", rows, err)
	}
}

// TestD383C2_StreakResetOnInterrupt C2：streak=2 时中断一轮（band 外）→ 清零重计。
func TestD383C2_StreakResetOnInterrupt(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedUniVol(t, s, "binance", "GUSDT", "TRADING", "T3", 72*time.Hour, 92, 100e6)
	es := NewEntryStreak()

	if opened := stepOpen(t, s, es, now); len(opened) != 0 { // r1 streak=1
		t.Fatalf("r1 opened=%v", opened)
	}
	if opened := stepOpen(t, s, es, now.Add(time.Hour)); len(opened) != 0 { // r2 streak=2
		t.Fatalf("r2 opened=%v", opened)
	}
	// r3 中断：funding 出 band（92→5）
	if _, err := s.Pool().Exec(ctx, `UPDATE fin_cex_universe SET last_funding_ann = 5 WHERE symbol = 'GUSDT'`); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if opened := stepOpen(t, s, es, now.Add(2*time.Hour)); len(opened) != 0 {
		t.Fatalf("r3 opened=%v", opened)
	}
	// r4-r6 恢复达标：清零后第 3 轮（r6）才开——若未清零 r4 即开（红）
	if _, err := s.Pool().Exec(ctx, `UPDATE fin_cex_universe SET last_funding_ann = 92 WHERE symbol = 'GUSDT'`); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if opened := stepOpen(t, s, es, now.Add(3*time.Hour)); len(opened) != 0 {
		t.Fatalf("r4 opened=%v, want 空（中断应清零重计，红=清零缺失）", opened)
	}
	if opened := stepOpen(t, s, es, now.Add(4*time.Hour)); len(opened) != 0 {
		t.Fatalf("r5 opened=%v, want 空（重计第 2 轮）", opened)
	}
	if opened := stepOpen(t, s, es, now.Add(5*time.Hour)); len(opened) != 1 || opened[0] != "GUSDT" {
		t.Fatalf("r6 opened=%v, want [GUSDT]（重计第 3 轮放行）", opened)
	}
}

// TestD383C3_ExitLowStreakUnchanged C3 回归锚：退出 low_streak≥2 行为不变
// （含「一轮恢复即清零」既有语义）。
func TestD383C3_ExitLowStreakUnchanged(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedUniVol(t, s, "binance", "GUSDT", "TRADING", "T3", 72*time.Hour, 92, 100e6)
	opened := stepOpen(t, s, eligibleStreak("binance|GUSDT"), now)
	if len(opened) != 1 {
		t.Fatalf("opened=%v, want [GUSDT]", opened)
	}
	setF := func(f float64) {
		if _, err := s.Pool().Exec(ctx, `UPDATE fin_cex_universe SET last_funding_ann = $1 WHERE symbol = 'GUSDT'`, f); err != nil {
			t.Fatalf("set funding %v: %v", f, err)
		}
	}
	// r2：低于退出线 → low_streak=1，不退
	setF(5)
	if _, exited, err := StepPaperSpotCarry(ctx, s, NewEntryStreak(), now.Add(time.Hour)); err != nil || exited != 0 {
		t.Fatalf("r2 exited=%d err=%v, want 0（streak 1 未达退出线）", exited, err)
	}
	// r3：恢复 ≥10 → low_streak 清零，不退（既有中断语义）
	setF(50)
	if _, exited, err := StepPaperSpotCarry(ctx, s, NewEntryStreak(), now.Add(2*time.Hour)); err != nil || exited != 0 {
		t.Fatalf("r3 exited=%d err=%v, want 0（恢复应清零）", exited, err)
	}
	// r4：再低于 → streak=1，不退
	setF(5)
	if _, exited, err := StepPaperSpotCarry(ctx, s, NewEntryStreak(), now.Add(3*time.Hour)); err != nil || exited != 0 {
		t.Fatalf("r4 exited=%d err=%v, want 0", exited, err)
	}
	// r5：连续第 2 轮低于 → exit normalized（原语义阈值不变）
	if _, exited, err := StepPaperSpotCarry(ctx, s, NewEntryStreak(), now.Add(4*time.Hour)); err != nil || exited != 1 {
		t.Fatalf("r5 exited=%d err=%v, want 1（low_streak≥2 normalized）", exited, err)
	}
	var reason string
	if err := s.Pool().QueryRow(ctx, `SELECT exit_reason FROM paper_spot_carry WHERE symbol = 'GUSDT'`).Scan(&reason); err != nil {
		t.Fatalf("query: %v", err)
	}
	if reason != "normalized" {
		t.Errorf("exit_reason = %q, want normalized", reason)
	}
}

// TestD383C4_RestartResetsStreak C4：重启（新 EntryStreak）后 streak=0 保守重计，不依赖持久化。
func TestD383C4_RestartResetsStreak(t *testing.T) {
	s := d350Store(t)
	now := time.Now().UTC()
	seedUniVol(t, s, "binance", "GUSDT", "TRADING", "T3", 72*time.Hour, 92, 100e6)
	es1 := NewEntryStreak()
	if opened := stepOpen(t, s, es1, now); len(opened) != 0 { // r1
		t.Fatalf("r1 opened=%v", opened)
	}
	if opened := stepOpen(t, s, es1, now.Add(time.Hour)); len(opened) != 0 { // r2（streak 已 2）
		t.Fatalf("r2 opened=%v", opened)
	}
	// 重启：新计数器从 0 起——r3 若复用 es1 应已开仓（红=重启未清零）
	es2 := NewEntryStreak()
	if opened := stepOpen(t, s, es2, now.Add(2*time.Hour)); len(opened) != 0 {
		t.Fatalf("r3 opened=%v, want 空（重启后 streak=0 保守重计）", opened)
	}
	if opened := stepOpen(t, s, es2, now.Add(3*time.Hour)); len(opened) != 0 {
		t.Fatalf("r4 opened=%v, want 空", opened)
	}
	if opened := stepOpen(t, s, es2, now.Add(4*time.Hour)); len(opened) != 1 || opened[0] != "GUSDT" {
		t.Fatalf("r5 opened=%v, want [GUSDT]（重启后第 3 轮放行）", opened)
	}
}

// TestD383C5_KeyIsolationByVenue C5：键=venue|symbol——venue A 达标 venue B 中断，
// B 清零不影响 A；同 symbol 不同 venue 计数互不串扰。
func TestD383C5_KeyIsolationByVenue(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedUniVol(t, s, "binance", "AAUSDT", "TRADING", "T3", 72*time.Hour, 92, 100e6)
	seedUniVol(t, s, "okx", "AAUSDT", "TRADING", "T3", 72*time.Hour, 92, 100e6)
	es := NewEntryStreak()

	if opened := stepOpen(t, s, es, now); len(opened) != 0 { // r1 双双 streak=1
		t.Fatalf("r1 opened=%v", opened)
	}
	// r2：okx 腿 vol 跌破流动性闸 → okx 键清零；binance streak=2 不受扰
	if _, err := s.Pool().Exec(ctx, `UPDATE fin_cex_universe SET last_vol24h = 5e6 WHERE venue = 'okx' AND symbol = 'AAUSDT'`); err != nil {
		t.Fatalf("okx vol down: %v", err)
	}
	if opened := stepOpen(t, s, es, now.Add(time.Hour)); len(opened) != 0 {
		t.Fatalf("r2 opened=%v", opened)
	}
	// r3：okx 恢复 → binance 连续第 3 轮开仓，okx（重计 streak=1）不开
	if _, err := s.Pool().Exec(ctx, `UPDATE fin_cex_universe SET last_vol24h = 100e6 WHERE venue = 'okx' AND symbol = 'AAUSDT'`); err != nil {
		t.Fatalf("okx vol up: %v", err)
	}
	opened := stepOpen(t, s, es, now.Add(2*time.Hour))
	if len(opened) != 1 || opened[0] != "AAUSDT" {
		t.Fatalf("r3 opened=%v, want [AAUSDT]（binance 达标放行；B 中断不影响 A）", opened)
	}
	rows, err := s.ListActivePaperSpotCarry(ctx)
	if err != nil || len(rows) != 1 || rows[0].Venue != "binance" {
		t.Fatalf("active=%v err=%v, want 仅 binance 一行（键级隔离）", rows, err)
	}
	// r4/r5：okx 独立重计，连续第 3 轮（r5）开仓——同 symbol 不同 venue 不串扰、binance 不重开
	if opened := stepOpen(t, s, es, now.Add(3*time.Hour)); len(opened) != 0 {
		t.Fatalf("r4 opened=%v, want 空（okx 重计第 2 轮）", opened)
	}
	if opened := stepOpen(t, s, es, now.Add(4*time.Hour)); len(opened) != 1 || opened[0] != "AAUSDT" {
		t.Fatalf("r5 opened=%v, want [AAUSDT]（okx 重计第 3 轮放行）", opened)
	}
	rows, err = s.ListActivePaperSpotCarry(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("active=%d err=%v, want binance+okx 各一行", len(rows), err)
	}
}

// TestD383C6_VanishThenReturn C6：候选从 uni 消失（清零）后回归 → streak 从 0 重计，
// 防陈旧数据回归即放行。
func TestD383C6_VanishThenReturn(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedUniVol(t, s, "binance", "GUSDT", "TRADING", "T3", 72*time.Hour, 92, 100e6)
	es := NewEntryStreak()
	if opened := stepOpen(t, s, es, now); len(opened) != 0 { // r1
		t.Fatalf("r1 opened=%v", opened)
	}
	if opened := stepOpen(t, s, es, now.Add(time.Hour)); len(opened) != 0 { // r2（streak=2）
		t.Fatalf("r2 opened=%v", opened)
	}
	// r3：候选从 uni 消失（雷达停更/摘牌瞬窗）→ 键清零
	if _, err := s.Pool().Exec(ctx, `DELETE FROM fin_cex_universe WHERE symbol = 'GUSDT'`); err != nil {
		t.Fatalf("vanish: %v", err)
	}
	if opened := stepOpen(t, s, es, now.Add(2*time.Hour)); len(opened) != 0 {
		t.Fatalf("r3 opened=%v, want 空（uni 无此候选）", opened)
	}
	// r4-r6：回归重插 → 从 0 重计第 3 轮（r6）才开——若未清零 r4 即开（红）
	seedUniVol(t, s, "binance", "GUSDT", "TRADING", "T3", 72*time.Hour, 92, 100e6)
	if opened := stepOpen(t, s, es, now.Add(3*time.Hour)); len(opened) != 0 {
		t.Fatalf("r4 opened=%v, want 空（回归应从 0 重计，红=消失清零缺失）", opened)
	}
	if opened := stepOpen(t, s, es, now.Add(4*time.Hour)); len(opened) != 0 {
		t.Fatalf("r5 opened=%v, want 空", opened)
	}
	if opened := stepOpen(t, s, es, now.Add(5*time.Hour)); len(opened) != 1 || opened[0] != "GUSDT" {
		t.Fatalf("r6 opened=%v, want [GUSDT]", opened)
	}
}

// TestEntryStreakStepDrop EntryStreak 小对象直测：计数/达标集/清零/Drop + 并发（race 灵敏面）。
func TestEntryStreakStepDrop(t *testing.T) {
	es := NewEntryStreak()
	if got := es.Step([]string{"a"}, 3); len(got) != 0 {
		t.Fatalf("Step#1 got=%v, want 空", got)
	}
	es.Step([]string{"a"}, 3)
	got := es.Step([]string{"a", "b"}, 3)
	if _, ok := got["a"]; !ok {
		t.Fatal("a 连续 3 轮应达标")
	}
	if _, ok := got["b"]; ok {
		t.Fatal("b 仅 1 轮不应达标")
	}
	if got = es.Step([]string{"b"}, 3); len(got) != 0 {
		t.Fatalf("a 缺席一轮应清零: %v", got)
	}
	es.Drop("b")
	if got = es.Step([]string{"b"}, 3); len(got) != 0 {
		t.Fatalf("Drop 后应从 0 重计: %v", got)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				es.Step([]string{fmt.Sprintf("k%d", i%3)}, 3)
				es.Drop(fmt.Sprintf("k%d", (i+1)%3))
			}
		}(i)
	}
	wg.Wait()
}
