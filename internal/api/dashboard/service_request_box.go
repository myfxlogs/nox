package dashboard

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	v1 "nox/internal/gen/dashboardv1"
)

// service_request_box.go —— D-268 M4.6：投资人页请求箱回执（fin_condition_gap
// requested 集读 + id+二态写回；回执唯一写面，态机判权全后端）。

// ListRequestBox 请求箱集：status='requested' 行 + 请求龄（COALESCE(requested_at,created_at) 起算）。
func (s *Service) ListRequestBox(ctx context.Context, _ *connect.Request[v1.ListRequestBoxRequest]) (*connect.Response[v1.ListRequestBoxResponse], error) {
	rows, err := s.store.ListRequestedGaps(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	now := time.Now()
	out := &v1.ListRequestBoxResponse{}
	for _, r := range rows {
		var age int64
		if t := r.RequestedAt; t != nil {
			age = int64(now.Sub(*t).Seconds())
		} else {
			age = int64(now.Sub(r.CreatedAt).Seconds())
		}
		out.Items = append(out.Items, &v1.RequestBoxItem{
			Id: r.ID, TemplateId: r.TemplateID, Kind: r.Kind, Subject: r.Subject,
			RequestText: r.RequestText, AgeSeconds: age,
		})
	}
	return connect.NewResponse(out), nil
}

// ResolveRequest 回执写面：id+fulfilled|waived → ResolveConditionGap（note=「投资人页回执」留痕）。
// UNSPECIFIED=invalid_argument；行已收口/不存在→applied=false 幂等不误报。
func (s *Service) ResolveRequest(ctx context.Context, req *connect.Request[v1.ResolveRequestRequest]) (*connect.Response[v1.ResolveRequestResponse], error) {
	m := req.Msg
	var fulfilled bool
	switch m.State {
	case v1.ResolveRequestState_RESOLVE_REQUEST_STATE_FULFILLED:
		fulfilled = true
	case v1.ResolveRequestState_RESOLVE_REQUEST_STATE_WAIVED:
		fulfilled = false
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("state 必填 fulfilled|waived"))
	}
	applied, err := s.store.ResolveConditionGap(ctx, m.Id, fulfilled, "投资人页回执")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.ResolveRequestResponse{Applied: applied}), nil
}
