package dashboard

// service_trx_txs.go —— TRX 页流水区读面（fin_trx_txs 直透）+ CMC 域
// 凭据缺口幂等落箱（票面⑤：CMC 表/凭据/装配三缺实锤——系统强制义务
// 落请求箱，kind=credential 幂等不重复建行）。
import (
	"context"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "nox/internal/gen/dashboardv1"
)

// ListTrxTxs fin_trx_txs 行读面（kind 空=全部；过滤下推 SQL——前端零聚合令）。
func (s *Service) ListTrxTxs(ctx context.Context, req *connect.Request[v1.ListTrxTxsRequest]) (*connect.Response[v1.ListTrxTxsResponse], error) {
	rows, err := s.store.ListTRXTxs(ctx, int(req.Msg.Limit), req.Msg.Kind)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := &v1.ListTrxTxsResponse{}
	for _, t := range rows {
		row := &v1.TrxTxRow{
			Id: t.ID, TxId: t.TxID, Kind: t.Kind,
			AmountTrx:   float64(t.AmountSun) / 1e6, // sun→TRX 折值落后端（前端零换算）
			CheatStatus: t.CheatStatus,
		}
		if t.Counterparty != nil {
			row.Counterparty = *t.Counterparty
		}
		row.Ts = timestamppb.New(t.Ts)
		row.FetchedAt = timestamppb.New(t.FetchedAt)
		out.Txs = append(out.Txs, row)
	}
	return connect.NewResponse(out), nil
}
