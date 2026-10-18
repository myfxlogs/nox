package dashboard

// service_hedge_flow.go —— ListHedgeFlow 读面（arb 同名件对拍，D-212 统一流水卡；
// 画骨缺面件解锁——trx/cmc 双源 UNION 归一，store.ListHedgeFlowPage 直透）。
import (
	"context"
	"fmt"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "nox/internal/gen/dashboardv1"
)

func (s *Service) ListHedgeFlow(ctx context.Context, req *connect.Request[v1.ListHedgeFlowRequest]) (*connect.Response[v1.ListHedgeFlowResponse], error) {
	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = 50
	}
	items, err := s.store.ListHedgeFlowPage(ctx, req.Msg.SourceFilter, limit, int(req.Msg.Offset))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list hedge flow: %w", err))
	}
	total, _ := s.store.ListHedgeFlowCount(ctx, req.Msg.SourceFilter) // 计数败=如实零（txs 仍返——读面容错径）
	resp := &v1.ListHedgeFlowResponse{Total: total}
	for _, it := range items {
		resp.Txs = append(resp.Txs, &v1.HedgeFlowItem{
			Source:       it.Source,
			Ts:           timestamppb.New(it.Ts),
			Kind:         it.Kind,
			AmountValue:  it.AmountValue,
			AmountUnit:   it.AmountUnit,
			Counterparty: it.Counterparty,
			TxId:         it.TxID,
		})
	}
	return connect.NewResponse(resp), nil
}
