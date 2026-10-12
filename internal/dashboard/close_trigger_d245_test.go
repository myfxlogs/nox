// D-245 缺口2：dashboard 手动 RPC 入口触发源持久化测试（reconcile/engine 见各自包）。
// DSN 门控 loud-FAIL（D-207）。
package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil/storetest"
)

func TestD245_LogCloseTrigger_DashboardEntry(t *testing.T) {
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM autotrade_logs WHERE reason='manual_rpc' AND opportunity_id='D-245-opp-dash'`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM positions WHERE opportunity_id LIKE 'D-245%'`)
	})

	// 种子 OPEN 持仓（ListActivePositions 可命中——命中路径带 opportunity_id/strategy 字段）。
	pos := &pgstore.Position{
		OpportunityID: "D-245-opp-dash",
		Type:          "CARRY",
		Status:        "OPEN",
		OpenedAt:      ptrTimeD245(time.Now().UTC()),
	}
	id, err := st.CreatePosition(ctx, pos)
	if err != nil {
		t.Fatalf("create position: %v", err)
	}
	pos.ID = id

	s := &Service{store: st}
	s.logCloseTrigger(ctx, id, "manual_rpc")

	var reason string
	var gotPosID *int64
	var strategy string
	err = st.Pool().QueryRow(ctx,
		`SELECT reason, position_id, COALESCE(strategy,'') FROM autotrade_logs WHERE status='CLOSE_TRIGGER' AND position_id=$1 ORDER BY id DESC LIMIT 1`, id).
		Scan(&reason, &gotPosID, &strategy)
	if err != nil {
		t.Fatalf("read log row: %v", err)
	}
	if reason != "manual_rpc" {
		t.Fatalf("reason = %q, want manual_rpc", reason)
	}
	if strategy != "CARRY" {
		t.Fatalf("strategy = %q, want CARRY（position 现值回填）", strategy)
	}
}

func ptrTimeD245(t time.Time) *time.Time { return &t }
