// service_dci.go —— D-200：个人持仓+DCI 订单看板读面（只读零写；
// response 截≤300 字符已在 SQL left() 层收口——前端零信任）。
package dashboard

import (
	"context"

	"connectrpc.com/connect"

	dashv1 "nox/internal/gen/dashboardv1"
)

func (s *Service) ListPersonalPositions(ctx context.Context, req *connect.Request[dashv1.ListPersonalPositionsRequest]) (*connect.Response[dashv1.ListPersonalPositionsResponse], error) {
	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.store.ListPersonalPositions(ctx, req.Msg.Status, limit, 0)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &dashv1.ListPersonalPositionsResponse{}
	for i := range rows {
		p := &rows[i]
		var qty, entry float64
		if p.Qty != nil {
			qty = *p.Qty
		}
		if p.EntryPrice != nil {
			entry = *p.EntryPrice
		}
		var opened int64
		if p.OpenedAt != nil {
			opened = p.OpenedAt.Unix()
		}
		resp.Positions = append(resp.Positions, &dashv1.PersonalPositionRow{
			Id: p.ID, Asset: p.Asset, Kind: p.Kind, PriceSymbol: p.PriceSymbol,
			Qty: qty, EntryPrice: entry, Status: p.Status, Note: p.Note, OpenedAt: opened,
		})
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) ListDCIOrders(ctx context.Context, req *connect.Request[dashv1.ListDCIOrdersRequest]) (*connect.Response[dashv1.ListDCIOrdersResponse], error) {
	rows, err := s.store.ListDCIOrders(ctx, int(req.Msg.Limit))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &dashv1.ListDCIOrdersResponse{}
	for i := range rows {
		o := &rows[i]
		var strike float64
		if o.Strike != nil {
			strike = *o.Strike
		}
		var tenor, propID int64
		if o.TenorDays != nil {
			tenor = int64(*o.TenorDays)
		}
		if o.ProposalID != nil {
			propID = *o.ProposalID
		}
		resp.Orders = append(resp.Orders, &dashv1.DCIOrderRow{
			Id: o.ID, RequestId: o.RequestID, Venue: o.Venue, TemplateId: o.TemplateID,
			ProductRef: o.ProductRef, OptType: o.OptType, Strike: strike,
			InvestCcy: o.InvestCcy, Qty: o.Qty, TenorDays: tenor,
			Status: o.Status, Response: o.Response, ProposalId: propID,
			CreatedAt: o.CreatedAt.Unix(),
		})
	}
	return connect.NewResponse(resp), nil
}
