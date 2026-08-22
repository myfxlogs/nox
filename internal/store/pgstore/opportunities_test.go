package pgstore

import (
	"context"
	"testing"
	"time"
)

// oppTest 构造中性机会行（D-047：无真实金额/数量/账户值，纯测试夹具）。
func oppTest(id, typ string) *Opportunity {
	now := time.Now()
	return &Opportunity{
		ID:        id,
		Type:      typ,
		VenueBuy:  "venue_a:spot",
		VenueSell: "venue_b:spot",
		PriceBuy:  100.0,
		PriceSell: 100.5,
		GrossBps:  20.0,
		NetBps:    12.3,
		QuoteTime: now,
		ExpiresAt: now.Add(time.Hour),
	}
}

// TestUpsertOpportunity_InsertedFlag 插入 → true；同 id 再 upsert（刷新）→ false；新 id → true。
// D-050 xmax=0 技巧回归锁：仅新插入才返回 true，供站内推送判定。
func TestUpsertOpportunity_InsertedFlag(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE opportunities CASCADE"); err != nil {
		t.Fatalf("TRUNCATE opportunities: %v", err)
	}

	// 1. 新插入 → true
	inserted, err := s.UpsertOpportunity(ctx, oppTest("CARRY:AAA:venue_a:venue_b:1000", "CARRY"))
	if err != nil {
		t.Fatalf("UpsertOpportunity insert: %v", err)
	}
	if !inserted {
		t.Errorf("first upsert want inserted=true, got %v", inserted)
	}

	// 2. 同 id 再 upsert（刷新既有行）→ false
	inserted, err = s.UpsertOpportunity(ctx, oppTest("CARRY:AAA:venue_a:venue_b:1000", "CARRY"))
	if err != nil {
		t.Fatalf("UpsertOpportunity refresh: %v", err)
	}
	if inserted {
		t.Errorf("same-id refresh want inserted=false, got %v", inserted)
	}

	// 3. 不同 id → true
	inserted, err = s.UpsertOpportunity(ctx, oppTest("CARRY:BBB:venue_c:venue_d:1001", "CARRY"))
	if err != nil {
		t.Fatalf("UpsertOpportunity new id: %v", err)
	}
	if !inserted {
		t.Errorf("new-id upsert want inserted=true, got %v", inserted)
	}
}

// TestUpsertOpportunity_ComboKeySingleRow D-080 S2：同组合键两次 upsert → 一行且指标更新；不同组合两行。
func TestUpsertOpportunity_ComboKeySingleRow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE opportunities CASCADE"); err != nil {
		t.Fatalf("TRUNCATE opportunities: %v", err)
	}
	// 同组合键两次 upsert（不同 netBps）
	o1 := oppTest("CARRY:AAA:venue_a:spot:venue_b:spot", "CARRY")
	o1.NetBps = 10.0
	if _, err := s.UpsertOpportunity(ctx, o1); err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	o2 := oppTest("CARRY:AAA:venue_a:spot:venue_b:spot", "CARRY")
	o2.NetBps = 25.0
	if _, err := s.UpsertOpportunity(ctx, o2); err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	// 不同组合
	o3 := oppTest("CARRY:BBB:venue_c:spot:venue_d:spot", "CARRY")
	if _, err := s.UpsertOpportunity(ctx, o3); err != nil {
		t.Fatalf("upsert 3: %v", err)
	}
	// 验证行数=2（同组合合并为一行）
	var n int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM opportunities").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("want 2 rows (1 combo + 1 different), got %d", n)
	}
	// 验证同组合指标已更新为最新值
	var netBps float64
	if err := s.pool.QueryRow(ctx, "SELECT net_bps FROM opportunities WHERE id=$1",
		"CARRY:AAA:venue_a:spot:venue_b:spot").Scan(&netBps); err != nil {
		t.Fatalf("select net_bps: %v", err)
	}
	if netBps != 25.0 {
		t.Errorf("want net_bps=25.0 (updated), got %v", netBps)
	}
	// 验证 last_seen 已更新
	var lastSeenCount int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM opportunities WHERE last_seen IS NOT NULL").Scan(&lastSeenCount); err != nil {
		t.Fatalf("select last_seen: %v", err)
	}
	if lastSeenCount != 2 {
		t.Errorf("want 2 rows with last_seen, got %d", lastSeenCount)
	}
}


func TestPurgeAckedAlerts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE alerts CASCADE"); err != nil {
		t.Fatalf("TRUNCATE alerts: %v", err)
	}

	// 插入 3 条告警：① acked+超期（应删）② acked+7 天内（不删）③ 未 acked+超期（不删）
	// ① acked + 8 天前
	if err := s.InsertAlert(ctx, nil, "info", "old-acked"); err != nil {
		t.Fatalf("InsertAlert 1: %v", err)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE alerts SET acked=true, ts=now()-interval '8 days' WHERE message='old-acked'"); err != nil {
		t.Fatalf("set old-acked: %v", err)
	}
	// ② acked + 1 天前
	if err := s.InsertAlert(ctx, nil, "info", "recent-acked"); err != nil {
		t.Fatalf("InsertAlert 2: %v", err)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE alerts SET acked=true, ts=now()-interval '1 day' WHERE message='recent-acked'"); err != nil {
		t.Fatalf("set recent-acked: %v", err)
	}
	// ③ 未 acked + 8 天前
	if err := s.InsertAlert(ctx, nil, "info", "old-unacked"); err != nil {
		t.Fatalf("InsertAlert 3: %v", err)
	}
	if _, err := s.pool.Exec(ctx, "UPDATE alerts SET acked=false, ts=now()-interval '8 days' WHERE message='old-unacked'"); err != nil {
		t.Fatalf("set old-unacked: %v", err)
	}

	// purge：应只删 ①
	n, err := s.PurgeAckedAlerts(ctx, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("PurgeAckedAlerts: %v", err)
	}
	if n != 1 {
		t.Errorf("purge count want 1, got %d", n)
	}

	// 验证剩余 2 条（②③）
	var remaining int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM alerts").Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 2 {
		t.Errorf("remaining alerts want 2, got %d", remaining)
	}

	// 幂等：再 purge 应删 0
	n, err = s.PurgeAckedAlerts(ctx, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("PurgeAckedAlerts 2nd: %v", err)
	}
	if n != 0 {
		t.Errorf("idempotent purge count want 0, got %d", n)
	}
}
