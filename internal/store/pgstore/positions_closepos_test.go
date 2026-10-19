package pgstore

// ClosePosition 事务弱点修回归锚（票面验收：注入 cost_model 写败→重试可完成不丢成本行）。
// 注入径=fin_cost_model 临时 CHECK(false) 约束——INSERT 必违约束败（确定性故障面，
// 无需 mock seam）；旧径此败=status 已提交+重试撞 ErrTerminalState，新径整 tx 回滚。

import (
	"context"
	"testing"
)

func TestClosePositionCostWriteFailureAtomic(t *testing.T) {
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `DELETE FROM position_legs; DELETE FROM positions; DELETE FROM fin_cost_model`); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('cptx-1','CARRY','OPEN',0)`); err != nil {
		t.Fatalf("seed pos: %v", err)
	}
	var posID int64
	if err := s.Pool().QueryRow(ctx, `SELECT id FROM positions WHERE opportunity_id='cptx-1'`).Scan(&posID); err != nil {
		t.Fatalf("pos id: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status)
		 VALUES ($1,1,'OPEN','binance','perp','BTCUSDT','BUY',1,50000,'FILLED')`, posID); err != nil {
		t.Fatalf("seed leg: %v", err)
	}

	// 注入：cost_model 写必败（CHECK(false)）。
	if _, err := s.Pool().Exec(ctx, `ALTER TABLE fin_cost_model ADD CONSTRAINT cptx_fail CHECK (false)`); err != nil {
		t.Fatalf("inject constraint: %v", err)
	}
	fee := 0.5
	if err := s.ClosePosition(ctx, posID, -1.25, &fee, "full"); err == nil {
		t.Fatal("ClosePosition 应败（cost 写违约束），得到 nil")
	}
	// 原子性断言：status 不可已提交为 CLOSED（旧径病灶=败后仓已平）。
	var status string
	if err := s.Pool().QueryRow(ctx, `SELECT status FROM positions WHERE id=$1`, posID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "OPEN" {
		t.Fatalf("写败后 status=%s，原子性破（应仍 OPEN 供重试）", status)
	}
	// 撤注入→重试走原守卫重进，须成功且成本行落账。
	if _, err := s.Pool().Exec(ctx, `ALTER TABLE fin_cost_model DROP CONSTRAINT cptx_fail`); err != nil {
		t.Fatalf("drop constraint: %v", err)
	}
	if err := s.ClosePosition(ctx, posID, -1.25, &fee, "full"); err != nil {
		t.Fatalf("重试应成功: %v", err)
	}
	var fills int64
	if err := s.Pool().QueryRow(ctx,
		`SELECT fills FROM fin_cost_model WHERE venue='binance' AND structure='CARRY'`).Scan(&fills); err != nil {
		t.Fatalf("cost 行缺席=账实断点仍在: %v", err)
	}
	if err := s.Pool().QueryRow(ctx, `SELECT status FROM positions WHERE id=$1`, posID).Scan(&status); err != nil || status != "CLOSED" {
		t.Fatalf("status=%s err=%v", status, err)
	}
}
