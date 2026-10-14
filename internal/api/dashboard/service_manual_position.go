// service_manual_position.go —— D-512：手工仓位台账 RPC（录入面零执行权限）。
// 写径=台账行维护（info 审计留痕）；读径只进资金全景展示，敞口/风控零消费。
package dashboard

import (
	"context"
	"fmt"
	"log"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func manualPositionProto(r *pgstore.ManualPosition) *v1.ManualPosition {
	return &v1.ManualPosition{
		Id: r.ID, VenueLabel: r.VenueLabel, Symbol: r.Symbol, Qty: r.Qty,
		PriceUsd: r.PriceUSD, Note: r.Note, Enabled: r.Enabled,
		CreatedAt: timestamppb.New(r.CreatedAt), UpdatedAt: timestamppb.New(r.UpdatedAt),
	}
}

func (s *Service) ListManualPositions(ctx context.Context, _ *connect.Request[v1.ListManualPositionsRequest]) (*connect.Response[v1.ListManualPositionsResponse], error) {
	rows, err := s.store.ListManualPositions(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*v1.ManualPosition, 0, len(rows))
	for i := range rows {
		out = append(out, manualPositionProto(&rows[i]))
	}
	return connect.NewResponse(&v1.ListManualPositionsResponse{Rows: out}), nil
}

// UpsertManualPosition 录入/编辑台账行。qty<=0/symbol 空→拒（store 层同校验双闸）。
func (s *Service) UpsertManualPosition(ctx context.Context, req *connect.Request[v1.UpsertManualPositionRequest]) (*connect.Response[v1.UpsertManualPositionResponse], error) {
	g := req.Msg
	r := &pgstore.ManualPosition{
		ID: g.Id, VenueLabel: g.VenueLabel, Symbol: g.Symbol,
		Qty: g.Qty, PriceUSD: g.PriceUsd, Note: g.Note, Enabled: g.Enabled,
	}
	if err := s.store.UpsertManualPosition(ctx, r); err != nil {
		return connect.NewResponse(&v1.UpsertManualPositionResponse{Ok: false, Error: err.Error()}), nil
	}
	if aerr := s.store.InsertAlert(ctx, nil, "info", fmt.Sprintf(
		"手工仓位台账 %s：%s %s qty=%.8g（%s）", map[bool]string{true: "更新", false: "录入"}[g.Id > 0],
		r.VenueLabel, r.Symbol, r.Qty, r.Note)); aerr != nil {
		log.Printf("[manual-position] alert: %v（行已落账，留痕败不阻塞）", aerr)
	}
	return connect.NewResponse(&v1.UpsertManualPositionResponse{Ok: true, Id: r.ID}), nil
}

func (s *Service) DeleteManualPosition(ctx context.Context, req *connect.Request[v1.DeleteManualPositionRequest]) (*connect.Response[v1.DeleteManualPositionResponse], error) {
	if err := s.store.DeleteManualPosition(ctx, req.Msg.GetId()); err != nil {
		return connect.NewResponse(&v1.DeleteManualPositionResponse{Ok: false, Error: err.Error()}), nil
	}
	if aerr := s.store.InsertAlert(ctx, nil, "info", fmt.Sprintf("手工仓位台账删除 id=%d", req.Msg.GetId())); aerr != nil {
		log.Printf("[manual-position] delete alert: %v", aerr)
	}
	return connect.NewResponse(&v1.DeleteManualPositionResponse{Ok: true}), nil
}
