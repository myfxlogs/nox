package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestD181_RetroStrategyHPR：§3 策略行 HPR 聚合——avg(net/notional)、覆盖度 n_ok/n_all、
// 与 AggregateStrategyStats 同排除口径（stats_status/skipped、PROBE、事故仓出局）。
func TestD181_RetroStrategyHPR(t *testing.T) {
	s := testAggregateStore(t)
	ctx := context.Background()
	now := time.Now()
	// positions.type 有 CHECK 约束——用真实 type，先清共享库同名 strategy_stats 行。
	if _, err := s.pool.Exec(ctx, `DELETE FROM strategy_stats WHERE strategy='XASSET_CARRY'`); err != nil {
		t.Fatalf("clean strategy_stats: %v", err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO strategy_stats (strategy, total_trades, wins, losses, total_pnl, avg_pnl, win_rate, disabled)
		 VALUES ('XASSET_CARRY', 3, 2, 1, 6, 2, 0.66, false)`); err != nil {
		t.Fatalf("insert strategy_stats: %v", err)
	}
	// 仓1：notional=1000、net=2 → hpr=0.002；仓2：notional NULL → n_all+1 不入 avg；
	// 仓3：stats_status=skipped → 整行出局（n_all 都不计）。
	mk := func(opp string, realized float64, notional *float64) int64 {
		var id int64
		if err := s.pool.QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, realized_pnl, notional_usd)
			 VALUES ($1, 'XASSET_CARRY', 'CLOSED', $2, $2, $3, $4) RETURNING id`,
			opp, now, realized, notional).Scan(&id); err != nil {
			t.Fatalf("insert position %s: %v", opp, err)
		}
		return id
	}
	not := 1000.0
	p1 := mk("D181-A", 2.0, &not)
	p2 := mk("D181-B", 5.0, nil)
	p3 := mk("D181-C", 9.0, &not)
	settle := func(posID int64, net float64, st string) {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO position_settlements (position_id, vkey, expected_usd, realized_usd, hold_days, settled_at, net_realized_usd, stats_status)
			 VALUES ($1, 'XASSET_CARRY:v', 10, $2, 1, now(), $2, $3)`, posID, net, st); err != nil {
			t.Fatalf("insert settlement pos=%d: %v", posID, err)
		}
	}
	settle(p1, 2.0, "applied")
	settle(p2, 5.0, "applied")
	settle(p3, 9.0, "skipped")

	rows, err := s.retroStrategyRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var r *RetroStrategyRow
	for i := range rows {
		if rows[i].Strategy == "XASSET_CARRY" {
			r = &rows[i]
		}
	}
	if r == nil {
		t.Fatal("XASSET_CARRY row missing")
	}
	if r.HprN != 1 || r.HprAll != 2 {
		t.Fatalf("覆盖度错: n=%d all=%d（skipped 行应整行出局）", r.HprN, r.HprAll)
	}
	if r.HprAvg == nil || *r.HprAvg < 0.0019 || *r.HprAvg > 0.0021 {
		t.Fatalf("hpr 应=2/1000=0.002, got %v", r.HprAvg)
	}
}
