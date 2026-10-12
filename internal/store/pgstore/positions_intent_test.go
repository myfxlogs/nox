package pgstore

import (
	"context"
	"errors"
	"testing"
)

// TestPositionIntentFields_Migration0048 D-159 W1a S1：migration 0048 E2E 验证。
// prepareTestSchema 已跑全量 migrate（含 0048），此处验证：
//  1. 新列存在（request_id/dedup_key/environment/state_version/uncertainty_reason）
//  2. 索引存在（positions_request_id_idx / positions_active_dedup_key_idx）
//  3. 重复 request_id 拒绝（唯一索引）
//  4. 重复 active dedup_key 拒绝（partial unique index WHERE status IN active）
//  5. CLOSED 行的 dedup_key 不阻止新 active（WHERE 条件过滤）
//
// DSN-gated：无 NOX_TEST_PG_DSN 跳过。
func TestPositionIntentFields_Migration0048(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions CASCADE`); err != nil {
		t.Fatalf("truncate positions: %v", err)
	}
	t.Cleanup(func() {
		// race ×3 并发测试隔离——本测试插多种状态行，测后清表防残留影响其他测试。
		_, _ = s.pool.Exec(context.Background(), `TRUNCATE positions CASCADE`)
	})

	// 1. 新列存在性：插一行带全部新列，验证列存在 + state_version 默认 1。
	var posID int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, expected_net_bps, request_id, dedup_key, environment, uncertainty_reason)
		VALUES ('opp-0048', 'SWAPARB', 'OPEN', 0, 'req-001', 'SWAPARB:XAUUSD:A:B', 'live', 'test')
		RETURNING id
	`).Scan(&posID)
	if err != nil {
		t.Fatalf("insert with new columns failed (列不存在?): %v", err)
	}

	// state_version 默认 1（未显式指定时）
	var sv int
	if err := s.pool.QueryRow(ctx, `SELECT state_version FROM positions WHERE id=$1`, posID).Scan(&sv); err != nil {
		t.Fatalf("select state_version: %v", err)
	}
	if sv != 1 {
		t.Errorf("state_version default want 1, got %d", sv)
	}

	// 2. 索引存在性：查 pg_indexes。
	for _, idx := range []string{"positions_request_id_idx", "positions_active_dedup_key_idx"} {
		var exists bool
		err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE indexname=$1)`, idx).Scan(&exists)
		if err != nil {
			t.Fatalf("check index %s: %v", idx, err)
		}
		if !exists {
			t.Errorf("index %s not found (migration 0048 未应用?)", idx)
		}
	}

	// 3. 重复 request_id 拒绝（唯一索引，WHERE request_id IS NOT NULL）。
	_, err = s.pool.Exec(ctx, `
		INSERT INTO positions (opportunity_id, type, status, expected_net_bps, request_id)
		VALUES ('opp-dup-req', 'SWAPARB', 'OPEN', 0, 'req-001')
	`)
	if err == nil {
		t.Error("duplicate request_id should be rejected by unique index")
	}

	// 4. 重复 active dedup_key 拒绝（partial unique WHERE status IN active）。
	//    第一行 dedup_key='SWAPARB:XAUUSD:C:D' status=OPEN 已活跃，第二行同 dedup_key status=OPEN 应拒。
	_, err = s.pool.Exec(ctx, `
		INSERT INTO positions (opportunity_id, type, status, expected_net_bps, dedup_key)
		VALUES ('opp-dedup-1', 'SWAPARB', 'OPEN', 0, 'SWAPARB:XAUUSD:C:D')
	`)
	if err != nil {
		t.Fatalf("insert first active dedup_key: %v", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO positions (opportunity_id, type, status, expected_net_bps, dedup_key)
		VALUES ('opp-dedup-2', 'SWAPARB', 'OPEN', 0, 'SWAPARB:XAUUSD:C:D')
	`)
	if err == nil {
		t.Error("duplicate active dedup_key should be rejected by partial unique index")
	}

	// 5. CLOSED 行的 dedup_key 不阻止新 active（WHERE status IN active 过滤）。
	//    插一行 CLOSED + dedup_key='SWAPARB:XAUUSD:E:F'，再插一行 OPEN + 同 dedup_key 应成功。
	_, err = s.pool.Exec(ctx, `
		INSERT INTO positions (opportunity_id, type, status, expected_net_bps, dedup_key, closed_at)
		VALUES ('opp-closed-dedup', 'SWAPARB', 'CLOSED', 0, 'SWAPARB:XAUUSD:E:F', now())
	`)
	if err != nil {
		t.Fatalf("insert CLOSED dedup_key: %v", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO positions (opportunity_id, type, status, expected_net_bps, dedup_key)
		VALUES ('opp-new-active-dedup', 'SWAPARB', 'OPEN', 0, 'SWAPARB:XAUUSD:E:F')
	`)
	if err != nil {
		t.Errorf("new active with dedup_key of CLOSED row should be allowed (partial index WHERE status IN active), got: %v", err)
	}

	// 6. request_id=NULL 允许多行（partial unique WHERE request_id IS NOT NULL）。
	for i := 0; i < 2; i++ {
		_, err := s.pool.Exec(ctx, `
			INSERT INTO positions (opportunity_id, type, status, expected_net_bps)
			VALUES ($1, 'SWAPARB', 'OPEN', 0)
		`, "opp-null-req-"+string(rune('a'+i)))
		if err != nil {
			t.Errorf("insert with NULL request_id #%d should be allowed: %v", i, err)
		}
	}
}

// TestClaimClose D-159 W1a R3-1：原子领取平仓权 E2E。
// OPEN/LEGGED → CLOSING 成功；OPENING/CLOSING/CLOSED/FAILED → ErrClaimFailed。
// DSN-gated：无 NOX_TEST_PG_DSN 跳过。
func TestClaimClose(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions CASCADE`); err != nil {
		t.Fatalf("truncate positions: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `TRUNCATE positions CASCADE`)
	})

	// 插入 6 种状态各 1 行，记录 id。
	statuses := []string{"OPEN", "LEGGED", "OPENING", "CLOSING", "CLOSED", "FAILED"}
	ids := make(map[string]int64)
	for _, st := range statuses {
		var id int64
		if err := s.pool.QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, expected_net_bps, closed_at)
			 VALUES ($1, 'SWAPARB', $2, 0, CASE WHEN $2 IN ('CLOSED') THEN now() ELSE NULL END)
			 RETURNING id`,
			"CLAIMTEST:"+st, st).Scan(&id); err != nil {
			t.Fatalf("insert %s: %v", st, err)
		}
		ids[st] = id
	}

	// case 1: OPEN → CLOSING 成功
	if err := s.ClaimClose(ctx, ids["OPEN"]); err != nil {
		t.Errorf("ClaimClose OPEN: want nil, got %v", err)
	}
	// case 2: LEGGED → CLOSING 成功
	if err := s.ClaimClose(ctx, ids["LEGGED"]); err != nil {
		t.Errorf("ClaimClose LEGGED: want nil, got %v", err)
	}
	// case 3: OPENING → 失败（ErrClaimFailed）
	if err := s.ClaimClose(ctx, ids["OPENING"]); !errors.Is(err, ErrClaimFailed) {
		t.Errorf("ClaimClose OPENING: want ErrClaimFailed, got %v", err)
	}
	// case 4: CLOSING → 失败（ErrClaimFailed，防重复领取）
	if err := s.ClaimClose(ctx, ids["CLOSING"]); !errors.Is(err, ErrClaimFailed) {
		t.Errorf("ClaimClose CLOSING: want ErrClaimFailed (防重复领取), got %v", err)
	}
	// case 5: CLOSED → 失败
	if err := s.ClaimClose(ctx, ids["CLOSED"]); !errors.Is(err, ErrClaimFailed) {
		t.Errorf("ClaimClose CLOSED: want ErrClaimFailed, got %v", err)
	}
	// case 6: FAILED → 失败
	if err := s.ClaimClose(ctx, ids["FAILED"]); !errors.Is(err, ErrClaimFailed) {
		t.Errorf("ClaimClose FAILED: want ErrClaimFailed, got %v", err)
	}
	// case 7: 已领取的 OPEN（现在是 CLOSING）再次领取 → 失败（原子性验证）
	if err := s.ClaimClose(ctx, ids["OPEN"]); !errors.Is(err, ErrClaimFailed) {
		t.Errorf("ClaimClose already-claimed OPEN (now CLOSING): want ErrClaimFailed, got %v", err)
	}
}
