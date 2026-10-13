// service_dept_reports_test.go —— M4.0：ListDeptReports 公司日报读面（真实 5434 迁移库）。
package dashboard

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

// TestListDeptReports 公司日报单键（dept='company' upsert 幂等覆盖——1 日 1 行）+
// 空日期回落最新有报日 + 指定日读回。
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
	if err := st.UpsertDeptReport(ctx, pgstore.DeptReport{Dept: "company", ReportDate: date, Body: "公司日报体", ElapsedMs: 5, PromptTokens: 1, CompletionTokens: 2}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	svc := &Service{store: st}
	resp, err := svc.ListDeptReports(ctx, connect.NewRequest(&v1.ListDeptReportsRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if resp.Msg.ReportDate != "2026-10-12" || len(resp.Msg.Reports) != 1 {
		t.Fatalf("resp=%+v", resp.Msg)
	}
	if resp.Msg.Reports[0].Dept != "company" || resp.Msg.Reports[0].PromptTokens != 1 {
		t.Fatalf("row=%+v", resp.Msg.Reports[0])
	}
	if len(resp.Msg.Dates) == 0 || resp.Msg.Dates[0] != "2026-10-12" {
		t.Fatalf("dates=%v", resp.Msg.Dates)
	}
	// upsert 幂等覆盖（同日重跑仍 1 行）。
	if err := st.UpsertDeptReport(ctx, pgstore.DeptReport{Dept: "company", ReportDate: date, Body: "公司日报体v2"}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	resp2, err := svc.ListDeptReports(ctx, connect.NewRequest(&v1.ListDeptReportsRequest{ReportDate: "2026-10-12"}))
	if err != nil || len(resp2.Msg.Reports) != 1 || resp2.Msg.Reports[0].Body != "公司日报体v2" {
		t.Fatalf("re-read err=%v rows=%+v", err, resp2.Msg.Reports)
	}
}
