// service_probe_approve.go —— D-048②：LLM 探针提案审批 RPC。
// ApproveProbe：proposed→approved（CAS）→ 以提案契约调 D-047 OpenProbe（同一守卫链，
// 含 ARB_PROBE_RPC_ENABLED——审批面不复制不削弱任何守卫）→ 成→running+关联仓，
// OpenProbe 拒（FAILED/err）→ 回 proposed + warn 告警。
// RejectProbe：proposed→rejected（CAS，理由落 gate_note）；绝不动仓绝不下单。
// 恰好一次：approve CAS 只有一并发者成功→OpenProbe 只调一次；重复 approve（状态已迁）
// 返回当前态幂等，不重放执行。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// callOpenProbe D-047 执行面（probeOpener 注入缝——测试 fake 计数一次调用；
// 生产=真实 s.OpenProbe 同一守卫链全过）。
func (s *Service) callOpenProbe(ctx context.Context, r *v1.OpenProbeRequest) (*v1.OpenProbeResponse, error) {
	if s.probeOpener != nil {
		return s.probeOpener(ctx, r)
	}
	resp, err := s.OpenProbe(ctx, connect.NewRequest(r))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// ApproveProbe 批准探针提案→开 PROBE 仓（全守卫走 D-047 OpenProbe）。
func (s *Service) ApproveProbe(ctx context.Context, req *connect.Request[v1.ApproveProbeRequest]) (*connect.Response[v1.ApproveProbeResponse], error) {
	r := req.Msg
	if s.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("store 未装配"))
	}
	if r.GetProposalId() <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("proposal_id 须 >0"))
	}
	if !probeDecisionRefRe.MatchString(r.GetDecisionRef()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("decision_ref 须为 D# 形（^D-\\d{3,}$）"))
	}
	p, err := s.store.ExplorerProbeProposalGet(ctx, r.GetProposalId())
	if err != nil {
		if errors.Is(err, pgstore.ErrNoProbeProposal) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("探针提案 #%d 不存在", r.GetProposalId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if p.Status != "proposed" { // 幂等重放/并发：返回当前态不重放执行
		return connect.NewResponse(&v1.ApproveProbeResponse{
			Status: p.Status, Message: fmt.Sprintf("提案已处 %s 态（不重放执行）", p.Status)}), nil
	}
	if err := validateProbeContractForOpen(p); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	// CAS 占单：proposed→approved 只有一并发者成功。
	ok, err := s.store.ProbeProposalApprove(ctx, p.ID, r.GetDecisionRef())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !ok {
		cur, gerr := s.store.ExplorerProbeProposalGet(ctx, p.ID)
		if gerr == nil && cur != nil {
			return connect.NewResponse(&v1.ApproveProbeResponse{
				Status: cur.Status, Message: "并发审批已被另一调用占单（未重放执行）"}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("CAS 后重读失败: %v", gerr))
	}
	c := p.Contract
	open, oerr := s.callOpenProbe(ctx, &v1.OpenProbeRequest{
		VenueKey: c.VenueKey, Canonical: c.Canonical, Side: c.Side, Qty: c.Qty,
		Purpose: c.Purpose, DecisionRef: r.GetDecisionRef(),
		AutoCloseAt: timestamppb.New(c.AutoCloseAt),
	})
	if oerr != nil || open == nil || open.GetStatus() == "FAILED" {
		reason := "OpenProbe 调用异常"
		if oerr != nil {
			reason = oerr.Error()
		} else if open != nil {
			reason = open.GetMessage()
		}
		if _, rerr := s.store.ProbeProposalReset(ctx, p.ID); rerr != nil {
			log.Printf("[probe] proposal #%d reset after OpenProbe reject: %v", p.ID, rerr)
		}
		msg := fmt.Sprintf("[probe] 提案 #%d 批准但 OpenProbe 拒（回 proposed）: %s", p.ID, reason)
		if err := s.store.InsertAlert(ctx, nil, "warn", msg); err != nil {
			log.Printf("[probe] proposal #%d reject alert: %v", p.ID, err)
		}
		return connect.NewResponse(&v1.ApproveProbeResponse{Status: "PROPOSED", Message: msg}), nil
	}
	posID := open.GetPositionId()
	if posID <= 0 {
		if _, rerr := s.store.ProbeProposalReset(ctx, p.ID); rerr != nil {
			log.Printf("[probe] proposal #%d reset (no posID): %v", p.ID, rerr)
		}
		return connect.NewResponse(&v1.ApproveProbeResponse{
			Status: "PROPOSED", Message: "OpenProbe 未返回仓 id（回 proposed）"}), nil
	}
	if _, rerr := s.store.ProbeProposalRun(ctx, p.ID, posID); rerr != nil {
		// 仓已开而提案态迁移失败——仓在跑，critical 待人工校（不回退：单已下）。
		s.probeCritical(ctx, posID, fmt.Sprintf("提案 #%d approved→running 迁移失败（仓已开）: %v", p.ID, rerr))
		return connect.NewResponse(&v1.ApproveProbeResponse{
			Status: "RUNNING", Message: fmt.Sprintf("仓 #%d 已开但提案态迁移异常", posID), PositionId: posID}), nil
	}
	return connect.NewResponse(&v1.ApproveProbeResponse{
		Status: "RUNNING", Message: fmt.Sprintf("提案 #%d 已批准开仓 pos=%d", p.ID, posID), PositionId: posID}), nil
}

// validateProbeContractForOpen 批准前契约复读（防手工插行/历史行绕过 explorer 校验层）。
func validateProbeContractForOpen(p *pgstore.ProbeProposal) error {
	c := p.Contract
	if c.VenueKey == "" || c.Canonical == "" || c.Side == "" {
		return fmt.Errorf("提案 #%d 契约字段缺（venue_key/canonical/side）", p.ID)
	}
	if c.Qty <= 0 || c.NotionalUSD <= 0 {
		return fmt.Errorf("提案 #%d 契约 qty/notional 非正", p.ID)
	}
	if !c.AutoCloseAt.After(time.Now()) {
		return fmt.Errorf("提案 #%d auto_close_at 已过（%s）——重新提案", p.ID,
			c.AutoCloseAt.Format(time.RFC3339))
	}
	return nil
}

// RejectProbe 拒绝探针提案（绝不开仓绝不下单；理由落 gate_note 审计）。
func (s *Service) RejectProbe(ctx context.Context, req *connect.Request[v1.RejectProbeRequest]) (*connect.Response[v1.RejectProbeResponse], error) {
	r := req.Msg
	if s.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("store 未装配"))
	}
	if r.GetProposalId() <= 0 || r.GetReason() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("proposal_id/reason 必填"))
	}
	p, err := s.store.ExplorerProbeProposalGet(ctx, r.GetProposalId())
	if err != nil {
		if errors.Is(err, pgstore.ErrNoProbeProposal) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("探针提案 #%d 不存在", r.GetProposalId()))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if p.Status != "proposed" {
		return connect.NewResponse(&v1.RejectProbeResponse{
			Status: p.Status, Message: fmt.Sprintf("提案已处 %s 态（不可拒）", p.Status)}), nil
	}
	ok, err := s.store.ProbeProposalReject(ctx, p.ID, r.GetReason())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !ok {
		return connect.NewResponse(&v1.RejectProbeResponse{
			Status: p.Status, Message: "并发流转冲突（未拒绝）"}), nil
	}
	return connect.NewResponse(&v1.RejectProbeResponse{
		Status: "REJECTED", Message: fmt.Sprintf("提案 #%d 已拒绝", p.ID)}), nil
}
