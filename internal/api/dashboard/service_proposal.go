// service_proposal.go —— D-189：提案执行台 RPC（ListProposals 实况+
// AckProposal 业主拒批；执行=飞书 go 口令径，本面零自动执行）。
package dashboard

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"

	"nox/internal/econkb"
	dashv1 "nox/internal/gen/dashboardv1"
)

func (s *Service) ListProposals(ctx context.Context, req *connect.Request[dashv1.ListProposalsRequest]) (*connect.Response[dashv1.ListProposalsResponse], error) {
	rows, err := s.store.ListProposals(ctx, req.Msg.Status, 200)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// 坑签名透出（续段①）：template_id→payoff_class join 后馈 MatchHits——
	// dci_buy_low(naked_short_vol) 类提案带⚠标注；匹配仍是呈现非闸（禁线）。
	tmplIDs := make([]string, 0, len(rows))
	for _, p := range rows {
		tmplIDs = append(tmplIDs, p.TemplateID)
	}
	payoffByTmpl, err := s.store.PayoffClassByTemplates(ctx, tmplIDs)
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
			PitHits: pitHitLabels(econkb.Input{
				PayoffClass: payoffByTmpl[p.TemplateID], VenueA: p.Venue,
			}),
		})
	}
	return connect.NewResponse(resp), nil
}

// AckProposal 业主回执（现仅 decline；执行走飞书 go/go N 口令——提案不自动执行）。
func (s *Service) AckProposal(ctx context.Context, req *connect.Request[dashv1.AckProposalRequest]) (*connect.Response[dashv1.AckProposalResponse], error) {
	if req.Msg.Action == "approve" {
		return s.approveProposal(ctx, req.Msg.ProposalId)
	}
	if req.Msg.Action != "decline" {
		return connect.NewResponse(&dashv1.AckProposalResponse{
			Message: "action 仅支持 decline/approve（批准=D-661 决策席直批轨）"}), nil
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

// SetProposalGoRunner approve 轨后端注入（D-661；wiring 期先于服务）。
func (s *Service) SetProposalGoRunner(fn func(ctx context.Context, id int64) (string, error)) {
	s.proposalGoFn = fn
}

// approveProposal D-661：approve=触发 GoProposal 同径执行（闸链零绕——
// ModeGate/PaperFirst/CheckDCI/jit 全过）。仅 status=proposed 行（幂等——
// 已裁行返拒）；审计行由 GoProposal 包壳自落（终态+耗时在案），此处补
// 裁决者注记行（裁决面=dashboard RPC approve）——双轨透出语义不动。
func (s *Service) approveProposal(ctx context.Context, id int64) (*connect.Response[dashv1.AckProposalResponse], error) {
	p, err := s.store.GetProposal(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if p.Status != "proposed" {
		return connect.NewResponse(&dashv1.AckProposalResponse{
			Status: p.Status, Message: "提案已定（" + p.Status + "）"}), nil
	}
	if s.proposalGoFn == nil {
		return connect.NewResponse(&dashv1.AckProposalResponse{
			Status: p.Status, Message: "approve 轨未接线（GoProposal 后端缺）"}), nil
	}
	out, err := s.proposalGoFn(ctx, id)
	if err != nil {
		return connect.NewResponse(&dashv1.AckProposalResponse{
			Status: "proposed", Message: "闸链拒：" + err.Error()}), nil // 拒因首句如实透出（不改状态）
	}
	return connect.NewResponse(&dashv1.AckProposalResponse{
		Status: "executed", Message: out}), nil
}
