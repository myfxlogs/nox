// d015_residual_gate_test.go —— D-015 ① 残腿闸验收（R1）：回放 #505/#506 时间线。
// #505 gate 腿 13:36 起裸露（仓 CLOSING）→ 14:58 #506 应被拒而非照常开仓。
// R1 修订：venue 降级集合撤离开仓判定（集合留 reconciler 观测——残腿清零后
// 陈旧降级标记若进闸 = 永久误拦）；闸只认 CLOSING∪LEGGED 实存。
// 需要 test-pg2 DSN（NOX_TEST_PG_DSN）。
package gate

import (
	"context"
	"strings"
	"testing"
)

func d015Intent() TradeIntent {
	return TradeIntent{
		Strategy: "CROSS_VENUE", Canonical: "XAUUSD",
		VenueBuy: "vA:mt5", VenueSell: "vB:mt5",
		Qty: 0.01, NetBps: 16.3,
		DedupKey: "XASSET_CARRY:XAUUSD:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) {
			return "EXECUTED", "", 506
		},
	}
}

// TestD015_ClosingBlocksNewOpen #505→#506：存在 CLOSING 仓 → 新 Submit 拒开。
func TestD015_ClosingBlocksNewOpen(t *testing.T) {
	s := setupGateAlertStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps, opened_at)
		 VALUES ('opp:505','XASSET_CARRY','CLOSING',16.3,now())`); err != nil {
		t.Fatalf("insert CLOSING pos: %v", err)
	}
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: false, Environment: ""}, nil)
	intent := d015Intent()
	res := g.Submit(ctx, &intent)
	if res.Status != "REJECTED" || !strings.Contains(res.Reason, "residual leg") {
		t.Fatalf("CLOSING 存在应拒开, got %s reason=%s", res.Status, res.Reason)
	}
}

// TestD015_LeggedBlocksNewOpen LEGGED 仓（残腿未定）→ 同拒。
func TestD015_LeggedBlocksNewOpen(t *testing.T) {
	s := setupGateAlertStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps, opened_at)
		 VALUES ('opp:505','XASSET_CARRY','LEGGED',16.3,now())`); err != nil {
		t.Fatalf("insert LEGGED pos: %v", err)
	}
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: false, Environment: ""}, nil)
	intent := d015Intent()
	res := g.Submit(ctx, &intent)
	if res.Status != "REJECTED" || !strings.Contains(res.Reason, "residual leg") {
		t.Fatalf("LEGGED 存在应拒开, got %s reason=%s", res.Status, res.Reason)
	}
}

// TestD015_NoResidualPasses 无 CLOSING∪LEGGED → 残腿闸放行（#505 已平后正常开市）。
func TestD015_NoResidualPasses(t *testing.T) {
	s := setupGateAlertStore(t)
	ctx := context.Background()
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: false, Environment: ""}, nil)
	intent := d015Intent()
	res := g.Submit(ctx, &intent)
	if res.Status != "EXECUTED" {
		t.Fatalf("无残腿应放行, got %s reason=%s", res.Status, res.Reason)
	}
}
