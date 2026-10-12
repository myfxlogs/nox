// service_dept_reports.go —— D-244：ListDeptReports 部门日报→经理摘要读面。
// report_date 空=最新有报日（pgstore.ListDeptReportDates 首行）；dates 侧栏近 30 日。
package dashboard

import (
	"context"
	"time"

	v1 "nox/internal/gen/dashboardv1"

	"connectrpc.com/connect"
)

// ListDeptReports 某日全部部报+经理摘要（dept 升序——ASCII manager 居首，前端自排展示）。
func (s *Service) ListDeptReports(ctx context.Context, req *connect.Request[v1.ListDeptReportsRequest]) (*connect.Response[v1.ListDeptReportsResponse], error) {
	dates, err := s.store.ListDeptReportDates(ctx, 30)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var date time.Time
	if req.Msg.ReportDate != "" {
		date, err = time.Parse("2006-01-02", req.Msg.ReportDate)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	} else if len(dates) > 0 {
		date = dates[0]
	}
	resp := &v1.ListDeptReportsResponse{}
	for _, d := range dates {
		resp.Dates = append(resp.Dates, d.Format("2006-01-02"))
	}
	if date.IsZero() {
		return connect.NewResponse(resp), nil
	}
	rows, err := s.store.ListDeptReports(ctx, date)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp.ReportDate = date.Format("2006-01-02")
	for _, r := range rows {
		resp.Reports = append(resp.Reports, &v1.DeptReport{
			Dept:             r.Dept,
			ReportDate:       r.ReportDate.Format("2006-01-02"),
			Body:             r.Body,
			ElapsedMs:        int32(r.ElapsedMs),
			PromptTokens:     int32(r.PromptTokens),
			CompletionTokens: int32(r.CompletionTokens),
			CreatedTs:        r.CreatedAt.Unix(),
		})
	}
	return connect.NewResponse(resp), nil
}
