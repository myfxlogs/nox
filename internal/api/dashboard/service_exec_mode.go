// service_exec_mode.go —— D-620⑦：执行模式 RPC 读写口（LIVE 授权唯一径）。
package dashboard

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	dashv1 "nox/internal/gen/dashboardv1"
	"nox/internal/risk/gate"
)

// GetExecutionMode D-620⑦：执行模式+live_gates 授权视图。
func (s *Service) GetExecutionMode(ctx context.Context, _ *connect.Request[dashv1.GetExecutionModeRequest]) (*connect.Response[dashv1.GetExecutionModeResponse], error) {
	if s.controlPlane == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("控制面未接线"))
	}
	v, err := s.execModeView(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&dashv1.GetExecutionModeResponse{View: v}), nil
}

// SetExecutionMode D-620⑦：执行模式写口——LIVE→GrantLive（授权落账），其余→SetMode（降级自动撤权）。
func (s *Service) SetExecutionMode(ctx context.Context, req *connect.Request[dashv1.SetExecutionModeRequest]) (*connect.Response[dashv1.SetExecutionModeResponse], error) {
	if s.controlPlane == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("控制面未接线"))
	}
	m := req.Msg
	var err error
	if m.GetMode() == gate.ModeLive {
		if m.ExpiresAt == nil || !m.ExpiresAt.AsTime().After(time.Now()) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("LIVE 授权 expires_at 必填且须在未来"))
		}
		err = s.controlPlane.GrantLive(ctx, m.GetActor(), m.GetReason(), m.GetEvidenceRef(), m.ExpiresAt.AsTime())
	} else {
		err = s.controlPlane.SetMode(ctx, m.GetMode(), m.GetActor(), m.GetReason())
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	v, verr := s.execModeView(ctx)
	if verr != nil {
		return nil, connect.NewError(connect.CodeInternal, verr)
	}
	return connect.NewResponse(&dashv1.SetExecutionModeResponse{View: v}), nil
}

// execModeView 组装 ExecutionModeView（CheckGate 结论+控制行 enabled+active live_gates 行投影）。
func (s *Service) execModeView(ctx context.Context) (*dashv1.ExecutionModeView, error) {
	mode, allowed, reason, gerr := s.controlPlane.CheckGate(ctx)
	if gerr != nil {
		return nil, gerr
	}
	ks, kerr := s.controlPlane.IsKillSwitchActive(ctx)
	if kerr != nil {
		return nil, kerr
	}
	enabled := !ks
	v := &dashv1.ExecutionModeView{Mode: mode, Enabled: enabled, Allowed: allowed, Reason: reason}
	g, lerr := s.controlPlane.ActiveLiveGate(ctx)
	if lerr != nil {
		return nil, lerr
	}
	if g != nil {
		v.GateOperator = g.Operator
		v.GateEvidenceRef = g.EvidenceRef
		v.GateConfirmedAt = timestamppb.New(g.ConfirmedAt)
		if g.ExpiresAt != nil {
			v.GateExpiresAt = timestamppb.New(*g.ExpiresAt)
		}
	}
	return v, nil
}
