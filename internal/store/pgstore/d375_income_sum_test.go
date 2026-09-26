// d375_income_sum_test.go —— D-375 S1：息差已收列实收/估两窄查询锚（先红后绿）。
// v2 审计钉：SumIncomeByPosVenue join position_legs 必带 phase='OPEN'——同 leg_no 有
// OPEN+CLOSE 两行（pos494 实证），漏则 SUM 翻倍（本文件第一条锚）。
package pgstore

import (
	"context"
	"fmt"
	"testing"
)

// d375SeedPosition 插入 OPEN 仓返回 id。
func d375SeedPosition(t *testing.T, s *Store, oppID string) int64 {
	t.Helper()
	var id int64
	if err := s.pool.QueryRow(context.Background(),
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ($1,'XASSET_CARRY','OPEN',0) RETURNING id`,
		oppID).Scan(&id); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	return id
}

// d375SeedLeg 插入腿行（phase=OPEN/CLOSE；OPEN+CLOSE 同 leg_no 双行=翻倍陷阱锚具）。
func d375SeedLeg(t *testing.T, s *Store, posID int64, legNo int16, phase, venue string) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status)
		 VALUES ($1,$2,$3,split_part($4,':',1),split_part($4,':',2),'SOLUSDT','BUY',1,100,'FILLED')`, posID, legNo, phase, venue); err != nil {
		t.Fatalf("seed leg: %v", err)
	}
}

// d375SeedIncome 插入 income 事件（posID nil=未匹配行，legNo 同步 nil）。
func d375SeedIncome(t *testing.T, s *Store, posID *int64, legNo *int16, amount float64, tranID int64) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO income_events (venue, venue_type, symbol, income_type, amount_usd, tran_id, ts, position_id, leg_no)
		 VALUES ('binance', 'perp', 'SOLUSDT', 'FUNDING_FEE', $1, $2, now(), $3, $4)`,
		amount, tranID, posID, legNo); err != nil {
		t.Fatalf("seed income: %v", err)
	}
}

// TestSumIncomeByPosVenue 锚1：关联行聚合 + phase='OPEN' 防翻倍 + position_id NULL 不计 + venue 全形。
func TestSumIncomeByPosVenue(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx,
		`TRUNCATE income_events, position_cashflows, positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	pos1 := d375SeedPosition(t, s, "XASSET_CARRY:SOLUSDT:a:b")
	pos2 := d375SeedPosition(t, s, "XASSET_CARRY:SOLUSDT:c:d")
	d375SeedLeg(t, s, pos1, 1, "OPEN", "binance:perp")
	d375SeedLeg(t, s, pos1, 1, "CLOSE", "binance:perp") // 同 leg_no CLOSE 行：漏 phase 谓词则 SUM 翻倍
	d375SeedLeg(t, s, pos2, 1, "OPEN", "gate:perp")
	one := int16(1)
	var nilLeg *int16
	d375SeedIncome(t, s, &pos1, &one, 0.5, 101)
	d375SeedIncome(t, s, &pos1, &one, 0.1458, 102) // pos1 合计 0.6458（494 实证值）
	d375SeedIncome(t, s, &pos2, &one, 0.73, 103)
	d375SeedIncome(t, s, nil, nilLeg, 9.99, 104) // 未匹配行：不计入

	got, err := s.SumIncomeByPosVenue(ctx, []int64{pos1, pos2})
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if v := got[fmt.Sprintf("%d\x00binance:perp", pos1)]; v < 0.6457 || v > 0.6459 {
		t.Fatalf("锚1: pos1×binance:perp = %v, want 0.6458（红=phase 谓词缺翻倍或聚合错）", v)
	}
	if v := got[fmt.Sprintf("%d\x00gate:perp", pos2)]; v < 0.7299 || v > 0.7301 {
		t.Fatalf("锚1: pos2×gate:perp = %v, want 0.73（venue 全形=legs.venue）", v)
	}
	if len(got) != 2 {
		t.Fatalf("锚1: map 行数 = %d, want 2（NULL position_id 行不计）: %v", len(got), got)
	}
}

// TestSumEstimatedCashflowsByPos 锚2：estimated 计入、actual/NULL 排除、kind 过滤（commission 不计）。
func TestSumEstimatedCashflowsByPos(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx,
		`TRUNCATE income_events, position_cashflows, positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	pos1 := d375SeedPosition(t, s, "XASSET_CARRY:SOLUSDT:a:b")
	pos2 := d375SeedPosition(t, s, "XASSET_CARRY:SOLUSDT:c:d")
	seed := func(kind, quality string, amount float64) {
		t.Helper()
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO position_cashflows (position_id, venue, venue_type, kind, amount_usd, quality, ts)
			 VALUES ($1,'binance','perp',$2,$3,$4,now())`, pos1, kind, amount, quality); err != nil {
			t.Fatalf("seed cashflow %s/%s: %v", kind, quality, err)
		}
	}
	seed("swap", "estimated", 2.0)
	seed("funding", "estimated", 2.63)   // 合计 4.63（493/494/495 实证量级）
	seed("swap", "actual", 99.0)         // actual：排除（实收归 income 列，不双计）
	seed("funding", "unknown", 7.0)      // unknown：排除
	seed("commission", "estimated", 5.0) // kind 过滤：commission 不计

	got, err := s.SumEstimatedCashflowsByPos(ctx, []int64{pos1, pos2})
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	if v := got[pos1]; v < 4.629 || v > 4.631 {
		t.Fatalf("锚2: pos1 estimated = %v, want 4.63", v)
	}
	if _, ok := got[pos2]; ok {
		t.Fatalf("锚2: pos2 无现金流不应在 map: %v", got)
	}
}
