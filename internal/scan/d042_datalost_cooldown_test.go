// d042_datalost_cooldown_test.go —— D-042 验收锚：
// T1 两次 data_lost 后第三轮不开（冷却+warn alert）；T2 报价连续性不足不开；
// T3 24h 后解冻重开。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// d042SeedAlive seed perp 腿 funding facts（报价连续性闸放行）；venue 参数化（T3 perp=gate）。
func d042SeedAlive(t *testing.T, s *pgstore.Store, now time.Time, venue string) {
	t.Helper()
	if err := s.InsertFacts(context.Background(), []fact.Fact{
		{Kind: fact.KindFunding, Venue: venue, Symbol: "XAUUSD", Value: 12.5, Unit: "pct_annualized", Ts: now, Src: venue + "_ws"},
	}); err != nil {
		t.Fatalf("seed alive funding: %v", err)
	}
}

// d042SeedDataLost seed data_lost 退出行（直接 SQL：InsertPaperSpotCarry 不含 exit 字段；
// n 行 exit_ts=now-2h·i，均落 24h 冷却窗内）。
func d042SeedDataLost(t *testing.T, s *pgstore.Store, now time.Time, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		exitTs := now.Add(-time.Duration(i+1) * 2 * time.Hour)
		if _, err := s.Pool().Exec(ctx, `INSERT INTO paper_spot_carry
			(venue_a, venue_type_a, venue_b, venue_type_b, symbol, side, structure,
			 entry_funding_ann, qty_usd, status, exit_ts, exit_reason, entry_ts)
			VALUES ('gate','spot','gate','perp','XAUUSD','short_perp','long_spot_short_perp',
			 0,1000,'exited',$1,'data_lost',$2)`, exitTs, now.Add(-72*time.Hour)); err != nil {
			t.Fatalf("seed data_lost row: %v", err)
		}
	}
}

// TestD042_TwoDataLostBlocksThirdOpen T1：24h 内两次 data_lost → 第三轮不开（冷却+warn alert）。
func TestD042_TwoDataLostBlocksThirdOpen(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `DELETE FROM paper_spot_carry WHERE symbol='XAUUSD'`); err != nil {
		t.Fatalf("clean paper rows: %v", err)
	}
	now := time.Now()
	d042SeedAlive(t, s, now, "gate")
	d042SeedDataLost(t, s, now, 2)
	d555SeedCombo(t, s, "XAUUSD", "gate:spot", "XAUUSD", "gate:perp")

	opened, _, err := StepPaperCexCarry(ctx, s, []CexPaperCand{d555Cand("XAUUSD", "gate:spot", "gate:perp", 12.5)}, now, d555Opts(), nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("两次 data_lost 后冷却应不开: opened=%v", opened)
	}
	// 冷却对数计入 selfwatch 日报异常项（alerts warn——flap continue 处计数告警）。
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE level='warn' AND message LIKE '%data_lost 冷却%'`).Scan(&n); err != nil {
		t.Fatalf("query alerts: %v", err)
	}
	if n == 0 {
		t.Fatal("冷却应插 warn alert（selfwatch 日报异常项）")
	}
}

// TestD042_ContinuityInsufficientBlocks T2：双腿近 1h 报价缺席（funding fact 缺）→ 连续性不足不开。
func TestD042_ContinuityInsufficientBlocks(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `DELETE FROM paper_spot_carry WHERE symbol='XAUUSD'`); err != nil {
		t.Fatalf("clean paper rows: %v", err)
	}
	now := time.Now()
	// 跨 run 残留 funding facts 会经 cexQuoteAlive 放行——清出「近 1h 无报价」被测面。
	if _, err := s.Pool().Exec(ctx, `DELETE FROM facts WHERE kind='funding' AND venue IN ('okx','gate')`); err != nil {
		t.Fatalf("clean funding facts: %v", err)
	}
	d555SeedCombo(t, s, "XAUUSD", "gate:spot", "XAUUSD", "gate:perp")
	// 不 seed funding facts——双腿近 1h 报价缺席（无活性证据）。

	opened, _, err := StepPaperCexCarry(ctx, s, []CexPaperCand{d555Cand("XAUUSD", "gate:spot", "gate:perp", 12.5)}, now, d555Opts(), nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 0 {
		t.Fatalf("报价连续性不足应不开: opened=%v", opened)
	}
}

// TestD042_ThawAfter24h T3：data_lost 行 exit_ts 全部 >24h 前 → 解冻重开。
func TestD042_ThawAfter24h(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `DELETE FROM paper_spot_carry WHERE symbol='XAUUSD'`); err != nil {
		t.Fatalf("clean paper rows: %v", err)
	}
	now := time.Now()
	// 跨 run 残留 funding facts 清理后重 seed（报价连续性闸放行）。
	if _, err := s.Pool().Exec(ctx, `DELETE FROM facts WHERE kind='funding' AND venue IN ('okx','gate')`); err != nil {
		t.Fatalf("clean funding facts: %v", err)
	}
	d042SeedAlive(t, s, now, "gate")
	// 3 行 data_lost 但 exit_ts 均 >24h 前（冷却窗外）→ 解冻。
	for i := 0; i < 3; i++ {
		if _, err := s.Pool().Exec(ctx, `INSERT INTO paper_spot_carry
			(venue_a, venue_type_a, venue_b, venue_type_b, symbol, side, structure,
			 entry_funding_ann, qty_usd, status, exit_ts, exit_reason, entry_ts)
			VALUES ('gate','spot','gate','perp','XAUUSD','short_perp','long_spot_short_perp',
			 0,1000,'exited',$1,'data_lost',$2)`, now.Add(-25*time.Hour), now.Add(-96*time.Hour)); err != nil {
			t.Fatalf("seed thaw rows: %v", err)
		}
	}
	d555SeedCombo(t, s, "XAUUSD", "gate:spot", "XAUUSD", "gate:perp")

	opened, _, err := StepPaperCexCarry(ctx, s, []CexPaperCand{d555Cand("XAUUSD", "gate:spot", "gate:perp", 12.5)}, now, d555Opts(), nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 1 || opened[0] != "XAUUSD" {
		t.Fatalf("24h 后解冻应重开: opened=%v", opened)
	}
}
