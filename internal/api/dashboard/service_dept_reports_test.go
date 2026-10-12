// service_dept_reports_test.go —— D-244：ListDeptReports RPC 读面（真实 5434 迁移库）。
package dashboard

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"

	v1 "nox/internal/gen/dashboardv1"

	"connectrpc.com/connect"
)

// TestListDeptReports 种子两行（部+manager）→ 指定日读回 + 空日期回落最新有报日。
func TestListDeptReports(t *testing.T) {
	st, err := pgstore.New(context.Background(), testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(st.Close)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE fin_dept_reports`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	date := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	if err := st.UpsertDeptReport(ctx, pgstore.DeptReport{Dept: "账本", ReportDate: date, Body: "财报体", ElapsedMs: 5, PromptTokens: 1, CompletionTokens: 2}); err != nil {
		t.Fatalf("upsert dept: %v", err)
	}
	if err := st.UpsertDeptReport(ctx, pgstore.DeptReport{Dept: "manager", ReportDate: date, Body: "公司日报体"}); err != nil {
		t.Fatalf("upsert mgr: %v", err)
	}
	svc := &Service{store: st}
	resp, err := svc.ListDeptReports(ctx, connect.NewRequest(&v1.ListDeptReportsRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if resp.Msg.ReportDate != "2026-10-12" || len(resp.Msg.Reports) != 2 {
		t.Fatalf("resp=%+v", resp.Msg)
	}
	if resp.Msg.Reports[0].Dept != "manager" || resp.Msg.Reports[1].PromptTokens != 1 {
		t.Fatalf("rows=%+v", resp.Msg.Reports)
	}
	if len(resp.Msg.Dates) == 0 || resp.Msg.Dates[0] != "2026-10-12" {
		t.Fatalf("dates=%v", resp.Msg.Dates)
	}
	// 指定日 + upsert 幂等覆盖（重跑语义）。
	if err := st.UpsertDeptReport(ctx, pgstore.DeptReport{Dept: "账本", ReportDate: date, Body: "财报体v2"}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	resp2, err := svc.ListDeptReports(ctx, connect.NewRequest(&v1.ListDeptReportsRequest{ReportDate: "2026-10-12"}))
	if err != nil || len(resp2.Msg.Reports) != 2 || resp2.Msg.Reports[1].Body != "财报体v2" {
		t.Fatalf("re-read err=%v rows=%+v", err, resp2.Msg.Reports)
	}
}
