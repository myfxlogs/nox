// service_hypothesis.go —— D-623：fin_opp_hypothesis 裁决 RPC 写口。
package dashboard

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	dashv1 "nox/internal/gen/dashboardv1"
)

// DecideHypothesis D-623①：假设裁决唯一写径——decision 枚举+actor/reason/evidence_ref
// 必填+status='proposed' 才受理（重判/不存在拒），alert info 留痕在事务内。
func (s *Service) DecideHypothesis(ctx context.Context, req *connect.Request[dashv1.DecideHypothesisRequest]) (*connect.Response[dashv1.DecideHypothesisResponse], error) {
	m := req.Msg
	d := m.GetDecision()
	if d != "approved" && d != "rejected" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("decision 须为 approved|rejected"))
	}
	// decided_by 禁 NULL/裸 manual——actor 必填且须可认责（同 SetExecutionMode 形）。
	if m.GetActor() == "" || m.GetActor() == "manual" || m.GetReason() == "" || m.GetEvidenceRef() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("actor/reason/evidence_ref 必填（actor 禁空禁 manual）"))
	}
	if err := s.store.DecideHypothesis(ctx, m.GetId(), d, m.GetActor(), m.GetReason(), m.GetEvidenceRef()); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&dashv1.DecideHypothesisResponse{
		Id: m.GetId(), Status: d, DecidedBy: m.GetActor(),
	}), nil
}
