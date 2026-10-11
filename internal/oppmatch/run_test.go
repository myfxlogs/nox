package oppmatch

import (
	"context"
	"testing"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// TestD187_RunOnceRealStore：真测试库首扫——≥1 提案或缺口（票面验收同型），
// 二轮幂等（缺口已 requested 零再推）。
func TestD187_RunOnceRealStore(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunOnce(ctx, st); err != nil {
		t.Fatal(err)
	}
	// 累计面≥1（测试库跨轮残留使"首扫"非首轮——断言总效果非本轮回传）。
	var gapRows, propAlerts int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM fin_condition_gap`).Scan(&gapRows); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message LIKE '机会提案%'`).Scan(&propAlerts); err != nil {
		t.Fatal(err)
	}
	if gapRows+propAlerts == 0 {
		t.Fatal("匹配零产出=静默病灶（缺口行+提案告警皆无）")
	}
	_, g2, err := RunOnce(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if g2 != 0 {
		t.Fatalf("二轮缺口应幂等零新请求, got %d", g2)
	}
}
