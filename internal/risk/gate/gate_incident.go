package gate

import (
	"context"
	"fmt"
)

// gate_incident.go —— D-032 ② 事故仓停新开（D-030 风控硬线）。
// ∃ 仓 id > ack 游标 且属事故族（exit_kind∈{legged_unwind,incident_close} ∨
// incident_attributed ∨ status∈{LEGGED,FAILED}）→ 拒开。
// ack 游标=app_config ARB_RISK_INCIDENT_ACK_ID（UpdateAppConfig 写库即效=热生效；
// DB 行缺席回落 cfg/env/boot 值）。查询失败 fail-closed 拒开（同检查 2-4 风格）。

// incidentGate 事故仓闸。返回 (result,true)=拒开。
func (g *Gate) incidentGate(ctx context.Context, intent *TradeIntent) (GateResult, bool) {
	ackID := int64(g.cfg.RiskIncidentAckID)
	if v, ok, err := g.store.AppConfigInt64(ctx, "ARB_RISK_INCIDENT_ACK_ID"); err != nil {
		g.rejectLog(ctx, *intent, fmt.Sprintf("incident ack read: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "incident ack read failed"}, true
	} else if ok {
		ackID = v
	}
	blocker, err := g.store.IncidentBlockerID(ctx, ackID)
	if err != nil {
		g.rejectLog(ctx, *intent, fmt.Sprintf("incident blocker: %v", err), false)
		return GateResult{Status: "REJECTED", Reason: "incident blocker check failed"}, true
	}
	if blocker > 0 {
		reason := fmt.Sprintf("incident position #%d unacked (ack_id=%d)", blocker, ackID)
		g.rejectLog(ctx, *intent, reason, false)
		return GateResult{Status: "REJECTED", Reason: reason}, true
	}
	return GateResult{}, false
}
