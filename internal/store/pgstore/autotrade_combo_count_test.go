// 热修测试：CountComboStopLosses 的 reason 过滤按生产真实落盘格式验证。
// 缺陷背景（9-8 483 实证）：CLOSE_TRIGGER 落库 reason 带 "exit: " 前缀（engine_scan.go
// logCloseTrigger 调用侧拼接），原 LIKE 'basis loss%'/'net swap%' 前缀匹配=0 命中。
// 本测试用真实格式夹具锁行为：exit: basis loss / exit: net swap 计数，manual 不计。
package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestCountComboStopLosses_RealReasonFormats 真实 reason 格式的 SQL 级过滤断言。
func TestCountComboStopLosses_RealReasonFormats(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE autotrade_logs CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	opp := "CARRY:AUDJPY:52993526:mt5:277842155:mt5"
	posID := int64(9901)
	rows := []AutotradeLog{
		{OpportunityID: opp, PositionID: &posID, Strategy: "CARRY", Canonical: "AUDJPY",
			Status: "CLOSE_TRIGGER", Reason: "exit: basis loss -6.3 bps (entry -1.3 → now -7.6)", Ts: now.Add(-2 * time.Hour)},
		{OpportunityID: opp, PositionID: &posID, Strategy: "CARRY", Canonical: "AUDJPY",
			Status: "CLOSE_TRIGGER", Reason: "exit: net swap 0.000 bps/day <= 0.0", Ts: now.Add(-1 * time.Hour)},
		{OpportunityID: opp, PositionID: &posID, Strategy: "CARRY", Canonical: "AUDJPY",
			Status: "CLOSE_TRIGGER", Reason: "manual close by owner", Ts: now.Add(-30 * time.Minute)},
		{OpportunityID: opp, PositionID: &posID, Strategy: "CARRY", Canonical: "AUDJPY",
			Status: "CLOSED", Reason: "closed realized_pnl=-2.46", Ts: now},
	}
	for i := range rows {
		if err := s.InsertAutotradeLog(ctx, &rows[i]); err != nil {
			t.Fatalf("insert log %d: %v", i, err)
		}
	}
	got, err := s.CountComboStopLosses(ctx, 30)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt, ok := got[opp]; !ok || cnt != 2 {
		t.Errorf("combo stop losses for %s: got (%v, %v), want 2（exit: basis loss + exit: net swap；manual 与 CLOSED 不计）", opp, cnt, ok)
	}
}

// TestCountComboStopLosses_WindowExpiry 窗口外过期不计。
func TestCountComboStopLosses_WindowExpiry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE autotrade_logs CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	opp := "SWAPARB:AUDJPY:52993526:mt5:277842155:mt5"
	// InsertAutotradeLog 不写 ts（默认 now()），窗口外历史行用原生 SQL 带 ts
	if _, err := s.pool.Exec(ctx, `INSERT INTO autotrade_logs (ts, opportunity_id, position_id, strategy, canonical, venue_buy, venue_type_buy, venue_sell, venue_type_sell, qty, status, reason, net_bps)
		VALUES ($1, $2, 9902, 'SWAPARB', 'AUDJPY', '52993526', 'mt5', '277842155', 'mt5', 0, 'CLOSE_TRIGGER', 'exit: basis loss -5.0 bps', 0)`,
		now.Add(-40*24*time.Hour), opp); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.CountComboStopLosses(ctx, 30)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("30d 窗口外行应不计：got %v", got)
	}
}
