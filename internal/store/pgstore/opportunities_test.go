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
