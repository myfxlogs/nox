package pgstore

import (
	"context"
	"testing"

	"arb/internal/capital"
	"arb/internal/testutil"
)

// D-193-B：CapitalAccount/Snapshot/Movement 持久化测试（DSN-gated）。
// 复用 prepareTestSchema + testutil.TestDSN 模式。

func testCapitalStore(t *testing.T) *Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(ctx, `TRUNCATE capital_accounts, capital_account_venue_links, capital_snapshots, ledger CASCADE`); err != nil {
		t.Fatalf("truncate capital tables: %v", err)
	}
	return s
}

// TestCreateCapitalAccount_PrincipalFixed：创建后 principal_usd=1000。
func TestCreateCapitalAccount_PrincipalFixed(t *testing.T) {
	s := testCapitalStore(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "test-acc-1", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	if acc.PrincipalUSD != 1000 {
		t.Errorf("principal_usd=%v, want 1000", acc.PrincipalUSD)
	}
	if acc.AccountKey != "test-acc-1" {
		t.Errorf("account_key=%q, want \"test-acc-1\"", acc.AccountKey)
	}
	if acc.Status != "ACTIVE" {
		t.Errorf("status=%q, want \"ACTIVE\"", acc.Status)
	}
}

// TestCreateCapitalAccount_DuplicateKey：同 account_key 二次创建不报错（ON CONFLICT DO NOTHING），返回同一账户。
func TestCreateCapitalAccount_DuplicateKey(t *testing.T) {
	s := testCapitalStore(t)
	ctx := context.Background()
	acc1, err := s.CreateCapitalAccount(ctx, "dup-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("first CreateCapitalAccount: %v", err)
	}
	acc2, err := s.CreateCapitalAccount(ctx, "dup-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("second CreateCapitalAccount: %v", err)
	}
	if acc1.AccountID != acc2.AccountID {
		t.Errorf("duplicate key returned different account_id: %d vs %d", acc1.AccountID, acc2.AccountID)
	}
}

// TestLinkVenueAccount_DuplicateLink：同 (account_id, venue_key) 二次链接不报错（ON CONFLICT DO NOTHING）。
func TestLinkVenueAccount_DuplicateLink(t *testing.T) {
	s := testCapitalStore(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "link-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	if err := s.LinkVenueAccount(ctx, acc.AccountID, "REDACTED_MT_ACCT:mt5", "demo"); err != nil {
		t.Fatalf("first LinkVenueAccount: %v", err)
	}
	if err := s.LinkVenueAccount(ctx, acc.AccountID, "REDACTED_MT_ACCT:mt5", "demo"); err != nil {
		t.Fatalf("second LinkVenueAccount (should be idempotent): %v", err)
	}
}

// TestAppendCapitalSnapshot_Idempotent：同 source_event_id 二次追加不重复。
func TestAppendCapitalSnapshot_Idempotent(t *testing.T) {
	s := testCapitalStore(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "snap-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	eventID := "evt-001"
	snap := capital.CapitalSnapshot{
		ScopeType:     "account",
		ScopeKey:      "snap-acc",
		AccountID:     &acc.AccountID,
		Source:        "broker",
		SourceEventID: &eventID,
		Quality:       "actual",
	}
	if err := s.AppendCapitalSnapshot(ctx, &snap); err != nil {
		t.Fatalf("first AppendCapitalSnapshot: %v", err)
	}
	if err := s.AppendCapitalSnapshot(ctx, &snap); err != nil {
		t.Fatalf("second AppendCapitalSnapshot (should be idempotent): %v", err)
	}
	var count int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM capital_snapshots WHERE source_event_id = $1`, eventID).Scan(&count); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if count != 1 {
		t.Errorf("snapshot count=%d, want 1 (idempotent)", count)
	}
}

// TestCurrentCapitalSnapshot_Latest：多快照返回最新。
func TestCurrentCapitalSnapshot_Latest(t *testing.T) {
	s := testCapitalStore(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "latest-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	// 插 3 个快照，无 source_event_id（避免幂等去重），source_ts 递增
	for i, ts := range []string{"2026-01-01", "2026-01-02", "2026-01-03"} {
		snap := capital.CapitalSnapshot{
			ScopeType: "account",
			ScopeKey:  "latest-acc",
			AccountID: &acc.AccountID,
			Source:    "broker",
			Quality:   "actual",
		}
		// 用 SQL 直接插，控制 source_ts
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO capital_snapshots (scope_type, scope_key, account_id, source, source_ts, quality)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			snap.ScopeType, snap.ScopeKey, snap.AccountID, snap.Source, ts, snap.Quality); err != nil {
			t.Fatalf("insert snapshot %d: %v", i, err)
		}
	}
	snap, err := s.CurrentCapitalSnapshot(ctx, acc.AccountID)
	if err != nil {
		t.Fatalf("CurrentCapitalSnapshot: %v", err)
	}
	// 最新 source_ts = 2026-01-03
	if snap.SourceTS == nil {
		t.Fatal("SourceTS is nil, want 2026-01-03")
	}
	// 检查日期部分
	gotDate := snap.SourceTS.UTC().Format("2006-01-02")
	if gotDate != "2026-01-03" {
		t.Errorf("latest snapshot source_ts date=%s, want 2026-01-03", gotDate)
	}
}

// TestAppendCapitalMovement_Idempotent：同 event_key 二次追加不重复。
func TestAppendCapitalMovement_Idempotent(t *testing.T) {
	s := testCapitalStore(t)
	ctx := context.Background()
	acc, err := s.CreateCapitalAccount(ctx, "move-acc", "USD", "demo")
	if err != nil {
		t.Fatalf("CreateCapitalAccount: %v", err)
	}
	eventKey := "move-evt-001"
	m := capital.CapitalMovement{
		AccountID: &acc.AccountID,
		Kind:      "deposit",
		EventKey:  &eventKey,
		Quality:   "actual",
		Source:    "manual",
		Note:      "test deposit",
	}
	if err := s.AppendCapitalMovement(ctx, &m); err != nil {
		t.Fatalf("first AppendCapitalMovement: %v", err)
	}
	if err := s.AppendCapitalMovement(ctx, &m); err != nil {
		t.Fatalf("second AppendCapitalMovement (should be idempotent): %v", err)
	}
	var count int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM ledger WHERE event_key = $1`, eventKey).Scan(&count); err != nil {
		t.Fatalf("count movements: %v", err)
	}
	if count != 1 {
		t.Errorf("movement count=%d, want 1 (idempotent)", count)
	}
}

// TestCapitalRepository_InterfaceSatisfied（D-193 审计 P0-4）：编译时验证 *Store 满足 capital.CapitalRepository。
func TestCapitalRepository_InterfaceSatisfied(t *testing.T) {
	var _ capital.CapitalRepository = (*Store)(nil)
}
