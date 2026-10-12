// D-245 缺口2：三入口触发源持久化测试（engine/dashboard 两入口；reconcile 入口见 close_governance_test.go）。
// DSN 门控 loud-FAIL（D-207）。
package autotrade

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

func TestD245_LogCloseTrigger_EngineEntry(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore: %v", err)
	}
	t.Cleanup(st.Close)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM autotrade_logs WHERE reason LIKE 'D-245%'`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM positions WHERE opportunity_id LIKE 'D-245%'`)
	})

	// 种子 position（字段面=引擎触发时可得的现值）。
	pos := &pgstore.Position{
		OpportunityID: "D-245-opp-engine",
		Type:          "CARRY",
		Status:        "OPEN",
		OpenedAt:      ptrTimeTest(time.Now().UTC()),
	}
	id, err := st.CreatePosition(ctx, pos)
	if err != nil {
		t.Fatalf("create position: %v", err)
	}
	pos.ID = id

	p := pos
	logCloseTrigger(ctx, st, p, "D-245 probe: 测试触发")

	var reason string
	var gotPosID *int64
	err = st.Pool().QueryRow(ctx,
		`SELECT reason, position_id FROM autotrade_logs WHERE status='CLOSE_TRIGGER' AND opportunity_id='D-245-opp-engine' ORDER BY id DESC LIMIT 1`).
		Scan(&reason, &gotPosID)
	if err != nil {
		t.Fatalf("read log row: %v", err)
	}
	if reason != "D-245 probe: 测试触发" {
		t.Fatalf("reason = %q", reason)
	}
	if gotPosID == nil || *gotPosID != id {
		t.Fatalf("position_id = %v, want %d", gotPosID, id)
	}
}

func ptrTimeTest(t time.Time) *time.Time { return &t }
