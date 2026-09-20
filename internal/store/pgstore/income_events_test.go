package pgstore

// D-334ab T3：income 行→OPEN 仓腿匹配三态——唯一命中（填 position_id/leg_no）、
// 多义（不猜 NULL）、零命中（NULL）；插入幂等见 collect/exchange T2（约束在本表 DDL）。
import (
	"context"
	"testing"
)

// insertLeg D-334 测试种子：给仓位插一条 OPEN FILLED 腿。
func insertLeg(t *testing.T, s *Store, posID int64, venue, symbol string, legNo int16) {
	t.Helper()
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO position_legs (position_id, leg_no, phase, venue, account, symbol, side, qty, price_expected, status)
		 VALUES ($1, $2, 'OPEN', $3, 'acc', $4, 'BUY', 1.0, 1.0, 'FILLED')`,
		posID, legNo, venue, symbol)
	if err != nil {
		t.Fatalf("insert leg %s/%s: %v", venue, symbol, err)
	}
}

func TestD334ab_MatchOpenLegForIncome(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// ① 唯一命中：pos A OPEN + binance/XAUUSDT 单腿
	posA, err := s.CreatePosition(ctx, &Position{OpportunityID: "T3A:CROSS_VENUE:a:b", Type: "CROSS_VENUE", Status: "OPEN"})
	if err != nil {
		t.Fatalf("create posA: %v", err)
	}
	insertLeg(t, s, posA, "binance:perp", "XAUUSDT", 2)
	posID, legNo, hits, err := s.MatchOpenLegForIncome(ctx, "binance:perp", "XAUUSDT", "live")
	if err != nil || hits != 1 || posID != posA || legNo != 2 {
		t.Fatalf("unique match: pos=%d leg=%d hits=%d err=%v, want posA/2/1/nil", posID, legNo, hits, err)
	}

	// ② 多义：再加两个同 venue+symbol 的 OPEN 腿 → hits>1 不猜
	posB, err := s.CreatePosition(ctx, &Position{OpportunityID: "T3B:CROSS_VENUE:c:d", Type: "CROSS_VENUE", Status: "OPEN"})
	if err != nil {
		t.Fatalf("create posB: %v", err)
	}
	insertLeg(t, s, posB, "binance:perp", "XAUUSDT", 1)
	posC, err := s.CreatePosition(ctx, &Position{OpportunityID: "T3C:CROSS_VENUE:e:f", Type: "CROSS_VENUE", Status: "OPEN"})
	if err != nil {
		t.Fatalf("create posC: %v", err)
	}
	insertLeg(t, s, posC, "binance:perp", "XAUUSDT", 1)
	_, _, hits, err = s.MatchOpenLegForIncome(ctx, "binance:perp", "XAUUSDT", "live")
	if err != nil || hits <= 1 {
		t.Fatalf("ambiguous: hits=%d err=%v, want >1", hits, err)
	}

	// ③ 零命中
	_, _, hits, err = s.MatchOpenLegForIncome(ctx, "binance:perp", "ETHUSDT", "live")
	if err != nil || hits != 0 {
		t.Fatalf("no match: hits=%d err=%v, want 0/nil", hits, err)
	}

	// ④ CLOSED 仓腿不算活跃（posA 翻 CLOSED 后不再命中）
	if _, err := s.Pool().Exec(ctx, `UPDATE positions SET status='CLOSED' WHERE id=$1`, posA); err != nil {
		t.Fatalf("close posA: %v", err)
	}
	_, _, hits, err = s.MatchOpenLegForIncome(ctx, "binance:perp", "XAUUSDT", "live")
	if err != nil || hits != 2 {
		t.Fatalf("closed excluded: hits=%d err=%v, want 2（B/C 两腿）", hits, err)
	}
}

// TestMatchOpenLegForIncome_EnvConsistency D-362 S5 锚：sim 配置事件（env=demo）只配
// environment='demo' 腿、real（env=live）只配 live 腿——跨环境不互配（NULL 归 live）。
func TestMatchOpenLegForIncome_EnvConsistency(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	posLive, err := s.CreatePosition(ctx, &Position{OpportunityID: "T362A:CROSS_VENUE:a:b", Type: "CROSS_VENUE", Status: "OPEN"})
	if err != nil {
		t.Fatalf("create live pos: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE positions SET environment='live' WHERE id=$1`, posLive); err != nil {
		t.Fatalf("set live: %v", err)
	}
	insertLeg(t, s, posLive, "binance:perp", "XAUUSDT", 1)
	posDemo, err := s.CreatePosition(ctx, &Position{OpportunityID: "T362B:CROSS_VENUE:c:d", Type: "CROSS_VENUE", Status: "OPEN"})
	if err != nil {
		t.Fatalf("create demo pos: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `UPDATE positions SET environment='demo' WHERE id=$1`, posDemo); err != nil {
		t.Fatalf("set demo: %v", err)
	}
	insertLeg(t, s, posDemo, "binance:perp", "XAUUSDT", 1)
	// live 环境查询只命中 live 腿（posLive/leg1），demo 腿不混入
	posID, legNo, hits, err := s.MatchOpenLegForIncome(ctx, "binance:perp", "XAUUSDT", "live")
	if err != nil || hits != 1 || posID != posLive || legNo != 1 {
		t.Fatalf("live env: pos=%d leg=%d hits=%d err=%v, want posLive/1/1/nil", posID, legNo, hits, err)
	}
	posID, legNo, hits, err = s.MatchOpenLegForIncome(ctx, "binance:perp", "XAUUSDT", "demo")
	if err != nil || hits != 1 || posID != posDemo || legNo != 1 {
		t.Fatalf("demo env: pos=%d leg=%d hits=%d err=%v, want posDemo/1/1/nil", posID, legNo, hits, err)
	}
}
