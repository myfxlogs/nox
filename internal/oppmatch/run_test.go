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

// TestD189_RunOnceProposalRows：提案实体化——稳定 id 告警+同模板不重开+过期并入扫描拍。
func TestD189_RunOnceProposalRows(t *testing.T) {
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
	if _, err := st.Pool().Exec(ctx, `DELETE FROM fin_proposal; DELETE FROM alerts`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RunOnce(ctx, st); err != nil {
		t.Fatal(err)
	}
	// 提案行数==告警数且告警携稳定 id（提案#N 形）。
	var rows, alerts int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM fin_proposal`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message ~ '机会提案#[0-9]+'`).Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if rows == 0 {
		t.Skip("无达标模板——提案面零产出环境")
	}
	if alerts == 0 {
		t.Fatal("提案告警未携稳定数据库 id（提案#N 形）")
	}
	// 二扫：同模板 proposed 不重开行（幂等真锚）。
	if _, _, err := RunOnce(ctx, st); err != nil {
		t.Fatal(err)
	}
	var rows2 int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM fin_proposal`).Scan(&rows2); err != nil {
		t.Fatal(err)
	}
	if rows2 != rows {
		t.Fatalf("二扫提案行应幂等: %d→%d", rows, rows2)
	}
	// 过期并入扫描拍——全模板置不可行（live 集空）→pending 全 expired。
	if n, err := st.ExpireProposalsExcept(ctx, nil); err != nil || n != rows {
		t.Fatalf("expire 并入拍 n=%d want %d: %v", n, rows, err)
	}
}
