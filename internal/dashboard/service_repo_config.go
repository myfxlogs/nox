package dashboard

// service_repo_config.go D-270 R2：逆回购提醒配置 RPC handler。
// GetRepoConfig 读 DB 单行配置；SetRepoConfig UPDATE id=1（后端校验，前端零信任）。

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// GetRepoConfig 读逆回购提醒配置（DB cnfi_repo_config id=1）。
func (s *Service) GetRepoConfig(ctx context.Context, req *connect.Request[v1.GetRepoConfigRequest]) (*connect.Response[v1.GetRepoConfigResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("store 未配置"))
	}
	cfg, err := s.store.GetRepoConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("get repo config: %w", err)
	}
	return connect.NewResponse(&v1.GetRepoConfigResponse{
		IdleCny:      cfg.IdleCny,
		MinReturnCny: cfg.MinReturnCny,
		ReminderHour: int32(cfg.ReminderHour),
		Enabled:      cfg.Enabled,
	}), nil
}

// SetRepoConfig 更新逆回购提醒配置（后端校验，前端零信任）。
func (s *Service) SetRepoConfig(ctx context.Context, req *connect.Request[v1.SetRepoConfigRequest]) (*connect.Response[v1.SetRepoConfigResponse], error) {
	if s.store == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("store 未配置"))
	}
	// 后端校验
	if req.Msg.IdleCny < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("idle_cny must be >= 0"))
	}
	if req.Msg.MinReturnCny < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("min_return_cny must be >= 0"))
	}
	if req.Msg.ReminderHour < 0 || req.Msg.ReminderHour > 23 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("reminder_hour must be 0-23"))
	}
	if err := s.store.SetRepoConfig(ctx, req.Msg.IdleCny, req.Msg.MinReturnCny, int(req.Msg.ReminderHour), req.Msg.Enabled); err != nil {
		return nil, fmt.Errorf("set repo config: %w", err)
	}
	return connect.NewResponse(&v1.SetRepoConfigResponse{Ok: true}), nil
}
