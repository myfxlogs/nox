// service_proposal.go —— D-189：提案执行台 RPC（ListProposals 实况+
// AckProposal 业主拒批；执行=飞书 go 口令径，本面零自动执行）。
package dashboard

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"

	dashv1 "arb/internal/gen/dashboardv1"
)

func (s *Service) ListProposals(ctx context.Context, req *connect.Request[dashv1.ListProposalsRequest]) (*connect.Response[dashv1.ListProposalsResponse], error) {
	rows, err := s.store.ListProposals(ctx, req.Msg.Status, 200)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &dashv1.ListProposalsResponse{}
	for _, p := range rows {
		pj, _ := json.Marshal(p.Params)
		var dec int64
		if p.DecidedAt != nil {
			dec = p.DecidedAt.Unix()
		}
		var apr float64
		if p.ExpectedAPR != nil {
			apr = *p.ExpectedAPR
		}
		resp.Proposals = append(resp.Proposals, &dashv1.Proposal{
			Id: p.ID, TemplateId: p.TemplateID, ExecTrack: p.ExecTrack,
			Venue: p.Venue, ParamsJson: string(pj), ExpectedApr: apr,
			Status: p.Status, CreatedAt: p.CreatedAt.Unix(), DecidedAt: dec,
		})
	}
	return connect.NewResponse(resp), nil
}

// AckProposal 业主回执（现仅 decline；执行走飞书 go/go N 口令——提案不自动执行）。
func (s *Service) AckProposal(ctx context.Context, req *connect.Request[dashv1.AckProposalRequest]) (*connect.Response[dashv1.AckProposalResponse], error) {
	if req.Msg.Action != "decline" {
		return connect.NewResponse(&dashv1.AckProposalResponse{
			Message: "action 仅支持 decline（执行走飞书 go 口令）"}), nil
	}
	p, err := s.store.GetProposal(ctx, req.Msg.ProposalId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if p.Status != "proposed" {
		return connect.NewResponse(&dashv1.AckProposalResponse{
			Status: p.Status, Message: "提案已定（" + p.Status + "）"}), nil
	}
	if err := s.store.SetProposalStatus(ctx, p.ID, "declined"); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&dashv1.AckProposalResponse{
		Status: "declined", Message: "已拒批"}), nil
}
