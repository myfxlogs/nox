// probe_decider.go —— D-176②：dashboard.Service ApproveProbe/RejectProbe →
// notify.ProbeDecider 适配器（notify 不 import dashboard/gen 防环，薄壳转换）。
package main

import (
	"context"

	"connectrpc.com/connect"

	"nox/internal/api/dashboard"
	dashv1 "nox/internal/gen/dashboardv1"
)

type probeDecider struct{ svc *dashboard.Service }

func (d probeDecider) ApproveProbe(ctx context.Context, proposalID int64, decisionRef string) (string, string, int64, error) {
	resp, err := d.svc.ApproveProbe(ctx, connect.NewRequest(&dashv1.ApproveProbeRequest{
		ProposalId: proposalID, DecisionRef: decisionRef,
	}))
	if err != nil {
		return "", "", 0, err
	}
	return resp.Msg.GetStatus(), resp.Msg.GetMessage(), resp.Msg.GetPositionId(), nil
}

func (d probeDecider) RejectProbe(ctx context.Context, proposalID int64, reason string) (string, string, error) {
	resp, err := d.svc.RejectProbe(ctx, connect.NewRequest(&dashv1.RejectProbeRequest{
		ProposalId: proposalID, Reason: reason,
	}))
	if err != nil {
		return "", "", err
	}
	return resp.Msg.GetStatus(), resp.Msg.GetMessage(), nil
}
