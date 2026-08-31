package pgstore

import (
	"context"
	"testing"
)

// TestPositionNotionalUSDBackfill D-179 S1：migration 0052 回填——活跃仓按 legs 动态计算 notional_usd。
// 种子 3 个活跃仓（OPEN legs qty=0.01 price~147）→ 手动跑回填 UPDATE → 验证非 NULL 且量级 $1.5K~$3K。
// 回填公式：sum(qty * 100000 * COALESCE(price_actual, price_expected) / 147.0)
// 0.01 × 100000 × 147 / 147 = $1000 单腿；双腿 = $2000（在 $1.5K~$3K 范围内）。
func TestPositionNotionalUSDBackfill(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 种子 3 个活跃仓 + 双腿（OPEN phase）
	for i, oppID := range []string{"SWAPARB:AUDJPY:A:B", "SWAPARB:GBPJPY:C:D", "SWAPARB:EURJPY:E:F"} {
		var posID int
		if err := s.pool.QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ($1, 'SWAPARB', 'OPEN', 0) RETURNING id`,
			oppID).Scan(&posID); err != nil {
			t.Fatalf("insert position %d: %v", i, err)
		}
		price := 147.0 + float64(i) // 147, 148, 149
		for legNo := 1; legNo <= 2; legNo++ {
			if err := s.CreatePositionLeg(ctx, &PositionLeg{
				PositionID: int64(posID), LegNo: int16(legNo), Phase: "OPEN",
				Venue: "v:mt5", Symbol: "AUDJPY", Side: "BUY", Qty: 0.01,
				PriceExpected: price, PriceActual: &price, Status: "FILLED",
			}); err != nil {
				t.Fatalf("insert leg %d-%d: %v", i, legNo, err)
			}
		}
	}
	// 手动跑回填（migration 在空表时已跑过=0 行，此处种子后重跑验证公式）
	if _, err := s.pool.Exec(ctx, `
		UPDATE positions p SET notional_usd = sub.n
		FROM (
			SELECT position_id, sum(qty * 100000 * COALESCE(price_actual, price_expected) / 147.0) AS n
			FROM position_legs WHERE phase = 'OPEN'
			GROUP BY position_id
		) sub
		WHERE p.id = sub.position_id AND p.status IN ('OPEN','OPENING','LEGGED','CLOSING')`); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	// 验证 3 个活跃仓 notional_usd 非 NULL 且量级 $1.5K~$3K
	rows, err := s.pool.Query(ctx,
		`SELECT notional_usd FROM positions WHERE status = 'OPEN' ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var n *float64
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if n == nil {
			t.Errorf("active position %d: notional_usd is NULL, want non-NULL", count)
			continue
		}
		if *n < 1500 || *n > 3000 {
			t.Errorf("active position %d: notional_usd=$%.2f, want $1500~$3000", count, *n)
		}
		count++
	}
	if count != 3 {
		t.Errorf("want 3 active positions, got %d", count)
	}
}

// TestCreatePosition_NotionalUSD D-179 S2：CreatePosition 写入 notional_usd（非 nil 时落盘）。
func TestCreatePosition_NotionalUSD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	notional := 2940.0
	id, err := s.CreatePosition(ctx, &Position{
		OpportunityID: "SWAPARB:TEST:A:B",
		Type:          "SWAPARB",
		Status:        "OPENING",
		NotionalUSD:   &notional,
	})
	if err != nil {
		t.Fatalf("create position: %v", err)
	}
	var got *float64
	if err := s.pool.QueryRow(ctx, `SELECT notional_usd FROM positions WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got == nil {
		t.Fatal("notional_usd is NULL, want 2940")
	}
	if *got != notional {
		t.Errorf("notional_usd want %.2f, got %.2f", notional, *got)
	}
}

// TestCreatePosition_NotionalUSDNull D-179 S2：CreatePosition 不传 notional → NULL（向后兼容）。
func TestCreatePosition_NotionalUSDNull(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	id, err := s.CreatePosition(ctx, &Position{
		OpportunityID: "OPP:NULL",
		Type:          "CROSS_VENUE",
		Status:        "OPENING",
	})
	if err != nil {
		t.Fatalf("create position: %v", err)
	}
	var got *float64
	if err := s.pool.QueryRow(ctx, `SELECT notional_usd FROM positions WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("notional_usd want NULL, got %.2f", *got)
	}
}

// TestListActivePositions_NotionalUSD D-179 S2（v2-A 高危）：SELECT+Scan 同步加 notional_usd。
// 漏改=NotionalUSD 恒 nil=Σ=0=额度门静默失效超开。构造数据单测测不出此缺陷，须 DB 集成测试。
func TestListActivePositions_NotionalUSD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 插入含 notional_usd 的活跃仓
	n1 := 2000.0
	id1, err := s.CreatePosition(ctx, &Position{
		OpportunityID: "SWAPARB:A:B:C", Type: "SWAPARB", Status: "OPEN", NotionalUSD: &n1,
	})
	if err != nil {
		t.Fatalf("create pos1: %v", err)
	}
	// 插入 NULL notional_usd 的活跃仓
	id2, err := s.CreatePosition(ctx, &Position{
		OpportunityID: "SWAPARB:D:E:F", Type: "SWAPARB", Status: "OPEN",
	})
	if err != nil {
		t.Fatalf("create pos2: %v", err)
	}
	active, err := s.ListActivePositions(ctx)
	if err != nil {
		t.Fatalf("ListActivePositions: %v", err)
	}
	found1, found2 := false, false
	for _, p := range active {
		if p.ID == id1 {
			found1 = true
			if p.NotionalUSD == nil {
				t.Fatal("pos1 NotionalUSD is nil, want 2000 (SELECT/Scan 漏改？)")
			}
			if *p.NotionalUSD != n1 {
				t.Errorf("pos1 NotionalUSD want %.2f, got %.2f", n1, *p.NotionalUSD)
			}
		}
		if p.ID == id2 {
			found2 = true
			if p.NotionalUSD != nil {
				t.Errorf("pos2 NotionalUSD want nil, got %.2f", *p.NotionalUSD)
			}
		}
	}
	if !found1 {
		t.Error("pos1 not found in ListActivePositions")
	}
	if !found2 {
		t.Error("pos2 not found in ListActivePositions")
	}
}

// TestUpdatePositionNotional D-179 S2：UpdatePositionNotional 写入 notional_usd（Execute 闭包用）。
func TestUpdatePositionNotional(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	id, err := s.CreatePosition(ctx, &Position{
		OpportunityID: "SWAPARB:UPD:A:B", Type: "SWAPARB", Status: "OPENING",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.UpdatePositionNotional(ctx, id, 2940.0); err != nil {
		t.Fatalf("update notional: %v", err)
	}
	var got *float64
	if err := s.pool.QueryRow(ctx, `SELECT notional_usd FROM positions WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got == nil || *got != 2940.0 {
		t.Errorf("notional_usd want 2940, got %v", got)
	}
}
