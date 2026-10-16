// econkb_test.go —— 经济经验库接线锚：负结算产行幂等 + 事故行不产 + 正收益不产。
package pgstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nox/internal/platform/testutil"
)

func TestEconPending_AppendDedupe(t *testing.T) {
	dir := t.TempDir()
	line := "- pos=1 net=-3.50 settled=2026-10-17 key=ek1"
	if err := econPendingAppend(dir, "ek1", line); err != nil {
		t.Fatal(err)
	}
	if err := econPendingAppend(dir, "ek1", line); err != nil { // 同 key 重跑
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, econPendingName))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "key=ek1"); n != 1 {
		t.Fatalf("同 key 重跑零重复断言败: %d 行", n)
	}
}

func TestEconPending_SettlementFlow(t *testing.T) {
	st, err := New(context.Background(), testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("NOX_ECONKB_DIR", dir)
	// 负结算行（non-incident）→ 待归纳产行（positions/positions_settlements
	// 行走真实写径列形——id 自动取）
	var pid int64
	if err := st.pool.QueryRow(ctx, `INSERT INTO positions (opportunity_id,type, status, opened_at, closed_at, realized_pnl)
		VALUES ('EA:NEG:opp','XASSET_CARRY','CLOSED',now(),now(),-3.5) RETURNING id`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO position_settlements
		(position_id,vkey,expected_usd,realized_usd,settled_at,net_realized_usd,incident_attributed,settlement_event_key)
		VALUES ($1,'XASSET:EA_NEG',10,-3.5,now(),-3.5,false,'ek-neg-9901')`, pid); err != nil {
		t.Fatal(err)
	}
	st.econPendingForSettlement(ctx, pid)
	b, err := os.ReadFile(filepath.Join(dir, econPendingName))
	if err != nil {
		t.Fatalf("负结算应产待归纳行: %v", err)
	}
	if !strings.Contains(string(b), "key=ek-neg-9901") {
		t.Fatal("待归纳行缺 key")
	}
	// 事故行（incident_attributed=true）→ 不产（事故走事故复盘径）
	var pid2 int64
	if err := st.pool.QueryRow(ctx, `INSERT INTO positions (opportunity_id,type, status, opened_at, closed_at, realized_pnl)
		VALUES ('EA:INC:opp','XASSET_CARRY','CLOSED',now(),now(),-5) RETURNING id`).Scan(&pid2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `INSERT INTO position_settlements
		(position_id,vkey,expected_usd,realized_usd,settled_at,net_realized_usd,incident_attributed,settlement_event_key)
		VALUES ($1,'XASSET:EA_INC',10,-5,now(),-5,true,'ek-inc-9902')`, pid2); err != nil {
		t.Fatal(err)
	}
	st.econPendingForSettlement(ctx, pid2)
	b2, _ := os.ReadFile(filepath.Join(dir, econPendingName))
	if strings.Contains(string(b2), "key=ek-inc-9902") {
		t.Fatal("事故归因行不应产待归纳")
	}
}
