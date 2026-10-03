// d050_paper_nocap_test.go —— D-050 验收锚：质量闸保留（stale/suspect 拒开）、
// 发现段冷却中不开（flap ReopenCooldown + D-042 data_lost 冷却两闸各自可拦）。
// 无槽全量开锚=TestD050_AllCleanOpen_NoCap（d548_slot_test.go 重写件，12 干净行全开）；
// 保底行不受影响锚=TestD410_GuaranteedSlot/GuaranteedFlap（不动）。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// d050Clean 清本票族行（D05% 前缀——席位测试库跨 run 持久）。
func d050Clean(t *testing.T, s *pgstore.Store) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(), `DELETE FROM paper_spot_carry WHERE symbol LIKE 'D05%'`); err != nil {
		t.Fatalf("clean D05 rows: %v", err)
	}
}

// TestD050_StaleRowsNotOpen stale/suspect 行拒开（质量闸），同轮干净行照开。
func TestD050_StaleRowsNotOpen(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	d050Clean(t, s)
	rows := []UniverseRow{
		d374Row("D05OK", "277842155", "binance", "long_cfd_short_perp", 20, ""),
		d374Row("D05SUS", "277842155", "binance", "long_cfd_short_perp", 30, "suspect"),
		d374Row("D05STL", "277842155", "binance", "long_cfd_short_perp", 25, "stale"),
	}
	opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 1 || opened[0] != "D05OK" {
		t.Fatalf("stale/suspect 应拒开: opened=%v", opened)
	}
}

// TestD050_DataLostCooldownDiscovery D-042 data_lost 冷却（发现段同闸）：
// ReopenCooldown=0 关 flap 径——两次 data_lost 后第三轮不开；仅一次不拦（阈值 ≥2）。
func TestD050_DataLostCooldownDiscovery(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	d050Clean(t, s)
	noFlap := d410Opts()
	noFlap.ReopenCooldown = 0
	row := func() []UniverseRow {
		return []UniverseRow{d374Row("D05DL", "277842155", "binance", "long_cfd_short_perp", 20, "")}
	}
	// keepAlive 保持 venue 非 silent（D-500），D05DL 本体缺席 → miss_streak 步进。
	keepAlive := []UniverseRow{d374Row("D05KA", "277842155", "binance", "long_cfd_short_perp", 0, "suspect")}
	missOut := func() { // 行缺席 3 轮 → data_lost 退出（D-474a S4 连缺阈值）
		for i := 0; i < 3; i++ {
			if _, _, err := StepPaperCfdCarry(ctx, s, keepAlive, time.Now(), noFlap, nil); err != nil {
				t.Fatalf("miss round: %v", err)
			}
		}
	}
	// 第一次 data_lost：开 → 缺席 3 轮 → 退。
	if _, _, err := StepPaperCfdCarry(ctx, s, row(), time.Now(), noFlap, nil); err != nil {
		t.Fatalf("open1: %v", err)
	}
	missOut()
	// 重开（data_lost 计数=1 <2 阈值不拦）→ 第二次 data_lost。
	if opened, _, err := StepPaperCfdCarry(ctx, s, row(), time.Now(), noFlap, nil); err != nil || len(opened) != 1 {
		t.Fatalf("一次 data_lost 不应拦: opened=%v err=%v", opened, err)
	}
	missOut()
	// 两次 data_lost → 第三轮不开（data_lost 冷却；flap 径已关=归因干净）。
	if opened, _, err := StepPaperCfdCarry(ctx, s, row(), time.Now(), noFlap, nil); err != nil || len(opened) != 0 {
		t.Fatalf("两次 data_lost 后应不开: opened=%v err=%v", opened, err)
	}
	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM paper_spot_carry WHERE symbol='D05DL' AND status='active'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("冷却期不应有 active 行: %d", n)
	}
}

// TestD050_DiscoveryFlapCooldown 发现段 flap 冷却（D-050 新增同口径）：
// normalized 退出行 24h 窗内不重开；过窗重开（exit_reason=normalized → data_lost 计数=0，
// 排除 data_lost 闸干扰，归因干净）。
func TestD050_DiscoveryFlapCooldown(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	d050Clean(t, s)
	if _, err := s.Pool().Exec(ctx, `INSERT INTO paper_spot_carry
		(venue_a, venue_b, symbol, side, structure,
		 entry_funding_ann, qty_usd, status, exit_ts, exit_reason, entry_ts)
		VALUES ('277842155','binance','D05FL','short_perp','long_cfd_short_perp',
		 0,1000,'exited', now()-interval '2 hours','normalized', now()-interval '26 hours')`); err != nil {
		t.Fatalf("seed exit row: %v", err)
	}
	rows := []UniverseRow{d374Row("D05FL", "277842155", "binance", "long_cfd_short_perp", 20, "")}
	if opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil); err != nil || len(opened) != 0 {
		t.Fatalf("flap 冷却内应不开: opened=%v err=%v", opened, err)
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE paper_spot_carry SET exit_ts=now()-interval '25 hours' WHERE symbol='D05FL'`); err != nil {
		t.Fatal(err)
	}
	if opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil); err != nil || len(opened) != 1 {
		t.Fatalf("过窗应重开: opened=%v err=%v", opened, err)
	}
}
