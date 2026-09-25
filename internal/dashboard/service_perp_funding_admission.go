// service_perp_funding_admission.go D-491 S6：PERP_FUNDING 证据准入 RPC——
// List 透传 perp_funding_admission 全行 / SetOverride 业主钉死三态写 + 快照刷新 /
// Reevaluate 手动复估（注入评估器入口，nil=未接线 fail-closed 报错）。
package dashboard

import (
	"context"
	"fmt"
	"log/slog"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/detector"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// pfAdmissionControl 准入评估器注入面（detector.PerpFundingAdmissionEvaluator 实现；
// dashboard 反向不依赖具体类型——同 SetXAssetReloader 接口注入先例）。
type pfAdmissionControl interface {
	Reevaluate(ctx context.Context) error
	RefreshSnapshot(ctx context.Context) error
}

// SetPFAdmissionControl 装配注入（cmd/core runServices；nil=RPC 降级报错不 panic）。
func (s *Service) SetPFAdmissionControl(c *detector.PerpFundingAdmissionEvaluator) {
	if c == nil {
		return
	}
	s.pfAdmissionControl = c
}

// ListPerpFundingAdmission 全行透传（前端「资金费准入」折叠区行源）。
func (s *Service) ListPerpFundingAdmission(ctx context.Context, _ *connect.Request[v1.ListPerpFundingAdmissionRequest]) (*connect.Response[v1.ListPerpFundingAdmissionResponse], error) {
	rows, err := s.store.ListPerpFundingAdmission(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*v1.PerpFundingAdmissionRow, 0, len(rows))
	for _, r := range rows {
		row := &v1.PerpFundingAdmissionRow{
			Canonical: r.Canonical, Status: r.Status, BestPair: r.BestPair,
			MeanSpread: r.MeanSpread, SpreadSd: r.SpreadSD, DirConsistency: r.DirConsistency,
			DepthMinUsd: r.DepthMinUSD, Samples: int32(r.Samples),
			OwnerOverride: r.OwnerOverride, Note: r.Note,
		}
		if r.EvaluatedAt != nil {
			row.EvaluatedAt = timestamppb.New(*r.EvaluatedAt)
		}
		row.UpdatedAt = timestamppb.New(r.UpdatedAt)
		out = append(out, row)
	}
	return connect.NewResponse(&v1.ListPerpFundingAdmissionResponse{Rows: out}), nil
}

// SetPerpFundingAdmissionOverride 业主钉死三态（”=清除/pinned=钉死准入/denied=钉死拒绝）。
// 写后 RefreshSnapshot 即时生效（检测器原子快照，不等下轮评估）；快照刷新失败不吞写库事实。
func (s *Service) SetPerpFundingAdmissionOverride(ctx context.Context, req *connect.Request[v1.SetPerpFundingAdmissionOverrideRequest]) (*connect.Response[v1.SetPerpFundingAdmissionOverrideResponse], error) {
	g := req.Msg
	resp := &v1.SetPerpFundingAdmissionOverrideResponse{}
	switch g.Override {
	case "", detector.OverridePinned, detector.OverrideDenied:
	default:
		resp.Error = fmt.Sprintf("override 只接受 ''/pinned/denied（收到 %q）", g.Override)
		return connect.NewResponse(resp), nil
	}
	if g.Canonical == "" {
		resp.Error = "canonical 不能为空"
		return connect.NewResponse(resp), nil
	}
	if err := s.store.SetPerpFundingOverride(ctx, g.Canonical, g.Override); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if s.pfAdmissionControl != nil {
		if err := s.pfAdmissionControl.RefreshSnapshot(ctx); err != nil {
			slog.Warn("pf-admission refresh after override fail", "canonical", g.Canonical, "err", err) // best-effort 不回滚
		}
	}
	resp.Ok = true
	return connect.NewResponse(resp), nil
}

// ReevaluatePerpFundingAdmission 手动复估（业主 UI 按钮；与事件重估同路径 evalMu 串行）。
func (s *Service) ReevaluatePerpFundingAdmission(ctx context.Context, _ *connect.Request[v1.ReevaluatePerpFundingAdmissionRequest]) (*connect.Response[v1.ReevaluatePerpFundingAdmissionResponse], error) {
	resp := &v1.ReevaluatePerpFundingAdmissionResponse{}
	if s.pfAdmissionControl == nil {
		resp.Error = "准入评估器未接线"
		return connect.NewResponse(resp), nil
	}
	if err := s.pfAdmissionControl.Reevaluate(ctx); err != nil {
		resp.Error = err.Error()
		return connect.NewResponse(resp), nil
	}
	resp.Ok = true
	return connect.NewResponse(resp), nil
}
