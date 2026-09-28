// service_calib.go —— D-533 S6：校准计分板只读 RPC（month_ym 空=当前北京历月）。
package dashboard

import (
	"context"
	"time"

	"arb/internal/explorer"
	v1 "arb/internal/gen/dashboardv1"
	"connectrpc.com/connect"
)

func (s *Service) ListCalibScoreboard(ctx context.Context, req *connect.Request[v1.ListCalibScoreboardRequest]) (*connect.Response[v1.ListCalibScoreboardResponse], error) {
	month := req.Msg.MonthYm
	if month == "" {
		month = time.Now().In(explorer.BJTime).Format("2006-01")
	}
	rows, err := s.store.ListCalibScoreboard(ctx, month)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &v1.ListCalibScoreboardResponse{}
	for _, r := range rows {
		resp.Rows = append(resp.Rows, &v1.CalibScoreRow{
			MonthYm: r.MonthYM, Dim: r.Dim, Venue: r.Venue, Symbol: r.Symbol,
			SampleN: int32(r.SampleN), Mae: r.MAE, Bias: r.Bias, P90Abs: r.P90Abs, Status: r.Status,
		})
	}
	return connect.NewResponse(resp), nil
}
