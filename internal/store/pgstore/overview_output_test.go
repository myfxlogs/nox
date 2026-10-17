package pgstore

import (
	"context"
	"testing"
	"time"
)

// 产出面补建验收锚：§6 归因聚合断言+§7 漏斗计数断言（真库；共享库纪律=
// d561 同款唯一键前缀+定点 DELETE，禁 TRUNCATE 共享表）。
// 需 NOX_TEST_PG_DSN，未设置时 skip（testStore 门禁）。

func ovFixup(t *testing.T, s *Store) {
	t.Helper()
	stmts := []string{
		`DELETE FROM position_settlements WHERE position_id IN (SELECT id FROM positions WHERE opportunity_id LIKE 'ovfix%')`,
		`DELETE FROM positions WHERE opportunity_id LIKE 'ovfix%'`,
	}
	for _, q := range stmts {
		if _, err := s.pool.Exec(context.Background(), q); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, q := range stmts {
			_, _ = s.pool.Exec(context.Background(), q)
		}
	})
}

func TestOverviewAttribution(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ovFixup(t, s)
	now := time.Now().UTC()
	seed := []struct {
		opp, strategy, otype string
		net                  float64
		settled              time.Time
	}{
		{"ovfix:a", "funding_carry", "CARRY", 12.5, now.Add(-24 * time.Hour)},
		{"ovfix:b", "funding_carry", "CARRY", -2.5, now.Add(-24 * time.Hour)},
		{"ovfix:c", "", "SWAPARB", 3.0, now.Add(-24 * time.Hour)},                // 未归组→unassigned:SWAPARB
		{"ovfix:d", "funding_carry", "CARRY", 99, now.Add(-40 * 24 * time.Hour)}, // 窗外剔除
	}
	for _, r := range seed {
		var pid int64
		var sk *string
		if r.strategy != "" {
			sk = &r.strategy
		}
		if err := s.pool.QueryRow(ctx, `
			INSERT INTO positions (opportunity_id, type, status, opened_at, strategy_key)
			VALUES ($1,$2,'CLOSED',$3,$4) RETURNING id`,
			r.opp, r.otype, now.Add(-2*24*time.Hour), sk).Scan(&pid); err != nil {
			t.Fatalf("seed position: %v", err)
		}
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO position_settlements (position_id, net_realized_usd, settled_at, completeness_status)
			VALUES ($1,$2,$3,'complete')`, pid, r.net, r.settled); err != nil {
			t.Fatalf("seed settlement: %v", err)
		}
	}
	rows, err := s.OverviewAttribution(ctx, now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatalf("OverviewAttribution: %v", err)
	}
	byStrat := map[string]OverviewAttribRow{}
	for _, r := range rows {
		byStrat[r.Strategy] = r
	}
	fc, ok := byStrat["funding_carry"]
	if !ok || fc.Positions != 2 || fc.Net30USD != 10 || fc.Wins != 1 || fc.Losses != 1 {
		t.Errorf("funding_carry 归因: %+v (rows=%v)", fc, rows)
	}
	ua, ok := byStrat["unassigned:SWAPARB"]
	if !ok || ua.Net30USD != 3 {
		t.Errorf("未归组行须落 type 名如实披露: %+v", ua)
	}
}

func TestOverviewFunnel(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ovFixup(t, s)
	now := time.Now().UTC()
	// 沙箱环锚：seed 一行 entry_ts 窗内纸面行（dedup 防串）。
	var paperN0 int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM paper_spot_carry WHERE entry_ts >= $1`, now.Add(-48*time.Hour)).Scan(&paperN0); err != nil {
		t.Fatalf("count baseline: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO paper_spot_carry (symbol, side, entry_funding_ann, qty_usd, venue_a, entry_ts)
		VALUES ('OVFIXUSDT','long',0.2,5000,'okx',$1)`, now.Add(-time.Hour)); err != nil {
		t.Fatalf("seed paper: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM paper_spot_carry WHERE symbol='OVFIXUSDT'`)
	})
	rows, err := s.OverviewFunnel(ctx, now.Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("OverviewFunnel: %v", err)
	}
	if len(rows) != 9 {
		t.Fatalf("漏斗环数: got %d, want 9（发现/评估/沙箱/执行/持仓/平仓/账本/复盘/淘汰）", len(rows))
	}
	byStage := map[string]OverviewFunnelRow{}
	for _, r := range rows {
		byStage[r.Stage] = r
	}
	sb := byStage["sandbox"]
	if sb.Count48h != paperN0+1 {
		t.Errorf("沙箱环 48h 计数: got %d, want %d（基线 %d+seed 1）", sb.Count48h, paperN0+1, paperN0)
	}
	if !byStage["elimination"].Cumulative {
		t.Error("淘汰环缺时戳列须 cumulative=true 如实标注")
	}
	if byStage["discovery"].Label == "" {
		t.Error("环中文名缺失")
	}
}
