package dashboard

// action_badge_test.go —— nav 行动徽标单值锚（fe-design 挂账小票）：
// count=pending 毕业数 + requested 请求箱数（后端 SQL 供单值，前端零聚合）。
import (
	"context"
	"testing"

	connect "connectrpc.com/connect"
	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

func TestGetActionBadge(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	must := func(q string, args ...any) {
		if _, err := st.Pool().Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, sym := range []string{"BADGE1", "BADGE2"} {
		if err := st.InsertPaperSpotCarry(ctx, &pgstore.PaperSpotCarry{VenueA: "gate", Symbol: sym, Side: "short_perp", EntryFundingAnn: 50, QtyUSD: 100}); err != nil {
			t.Fatal(err)
		}
	}
	defer st.Pool().Exec(ctx, `DELETE FROM paper_spot_carry WHERE symbol IN ('BADGE1','BADGE2')`) // 级联删毕业行
	must(`INSERT INTO carry_graduations (paper_id, symbol, structure, venue_a, venue_b, age_days, accrued_usd, status)
		SELECT id, symbol, 'cfd', 'gate', '', 3, 0, CASE symbol WHEN 'BADGE1' THEN 'pending' ELSE 'approved' END
		FROM paper_spot_carry WHERE symbol IN ('BADGE1','BADGE2')`)
	must(`INSERT INTO fin_condition_gap (kind, subject, request_text, status) VALUES
		('capital','badge锚','请划转','requested'),('capital','badge锚2','请划转2','fulfilled')`)
	defer func() {
		st.Pool().Exec(ctx, `DELETE FROM carry_graduations WHERE paper_id IN (901,902)`)
		st.Pool().Exec(ctx, `DELETE FROM fin_condition_gap WHERE request_text LIKE '请划转%' AND subject LIKE 'badge锚%'`)
	}()
	svc := &Service{store: st}
	resp, err := svc.GetActionBadge(ctx, connect.NewRequest(&v1.GetActionBadgeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var want int64
	mustQ := `SELECT (SELECT count(*) FROM carry_graduations WHERE status='pending')
		+ (SELECT count(*) FROM fin_condition_gap WHERE status='requested')`
	if err := st.Pool().QueryRow(ctx, mustQ).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if int64(resp.Msg.Count) != want || want < 1 {
		t.Fatalf("count=%d want=%d（pending 毕业+requested 箱行合计）", resp.Msg.Count, want)
	}
}
