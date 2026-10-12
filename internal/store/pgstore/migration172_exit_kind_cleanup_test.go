package pgstore

import (
	"context"
	"testing"

	"arb/migrations"
)

// TestD032_Migration172_ExitKindCleanup 迁移 0172 语义锚：
// #503/#504 形态（FAILED + legged_unwind + 全腿零成交）清 exit_kind；
// 有成交腿的 FAILED / LEGGED / incident_close 不动；幂等重放无副作用。
func TestD032_Migration172_ExitKindCleanup(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, position_settlements RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	mkPos := func(oppID, status, exitKind string) int64 {
		var id int64
		if err := s.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at, exit_kind)
			 VALUES ($1,'XASSET_CARRY',$2, now()-interval '1 hour', now(), $3) RETURNING id`,
			oppID, status, exitKind).Scan(&id); err != nil {
			t.Fatalf("seed %s: %v", oppID, err)
		}
		return id
	}
	mkLeg := func(posID int64, legNo int, status string) {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO position_legs (position_id, leg_no, phase, attempt_no, venue, venue_type, symbol, side, qty, price_expected, status)
			 VALUES ($1,$2,'OPEN',1,'okx','perp','XAU-USDT-SWAP','SELL',1,2500,$3)`,
			posID, legNo, status); err != nil {
			t.Fatalf("seed leg: %v", err)
		}
	}
	z503 := mkPos("D032:mig:z503", "FAILED", "legged_unwind") // 零成交 FAILED → 应清
	z504 := mkPos("D032:mig:z504", "FAILED", "legged_unwind") // 零成交 FAILED → 应清
	mkLeg(z503, 0, "FAILED")
	mkLeg(z503, 1, "FAILED")
	mkLeg(z504, 0, "FAILED")
	pFill := mkPos("D032:mig:fill", "FAILED", "legged_unwind") // ∃FILLED → 保留
	mkLeg(pFill, 0, "FILLED")
	mkLeg(pFill, 1, "FAILED")
	legg := mkPos("D032:mig:legged", "LEGGED", "legged_unwind") // LEGGED → 保留
	mkLeg(legg, 0, "FILLED")
	mkLeg(legg, 1, "FAILED")
	incc := mkPos("D032:mig:incc", "FAILED", "incident_close") // 非 legged_unwind → 不动
	mkLeg(incc, 0, "FAILED")

	for i := 0; i < 2; i++ { // 幂等重放
		if _, err := s.Pool().Exec(ctx, migrations.IncidentExitKindCleanupSQL); err != nil {
			t.Fatalf("replay 0172 (iter %d): %v", i, err)
		}
	}
	kind := func(id int64) *string {
		var k *string
		if err := s.Pool().QueryRow(ctx, `SELECT exit_kind FROM positions WHERE id=$1`, id).Scan(&k); err != nil {
			t.Fatalf("read exit_kind %d: %v", id, err)
		}
		return k
	}
	if k := kind(z503); k != nil {
		t.Fatalf("#503 形态应清 exit_kind, got %v", *k)
	}
	if k := kind(z504); k != nil {
		t.Fatalf("#504 形态应清 exit_kind, got %v", *k)
	}
	for _, c := range []struct {
		id   int64
		want string
	}{
		{pFill, "legged_unwind"}, {legg, "legged_unwind"}, {incc, "incident_close"},
	} {
		if k := kind(c.id); k == nil || *k != c.want {
			t.Fatalf("pos %d exit_kind 应保留 %s, got %v", c.id, c.want, k)
		}
	}
}
