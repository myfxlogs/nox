// service_proposal_approve_test.go —— AckProposal approve 轨三锚（D-661 返修②）：
// ①approve→proposalGoFn 同径调用锚；②裁决者审计行落 alerts（裁决者=
// dashboard RPC 与 feishu go 可分辨）；③幂等锚——status!=proposed 行返拒不调径。
package dashboard

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	dashv1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

func TestApproveProposal_Anchors(t *testing.T) {
	s := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM fin_proposal WHERE template_id LIKE 'tapprove_%'`,
		`DELETE FROM alerts WHERE message LIKE '%裁决审计%'`,
	} {
		if _, err := s.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
	pid, _, err := s.CreateProposal(ctx, &pgstore.Proposal{TemplateID: "tapprove_1", ExecTrack: "manual", Venue: "binance", Status: "proposed"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	svc := &Service{store: s}
	called := int64(-1)
	svc.SetProposalGoRunner(func(_ context.Context, id int64) (string, error) { called = id; return "已提交执行", nil })

	// ①同径调用锚
	resp, err := svc.AckProposal(ctx, connect.NewRequest(&dashv1.AckProposalRequest{ProposalId: pid, Action: "approve"}))
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if called != pid {
		t.Fatalf("proposalGoFn 未被 approve 同径调用：called=%d want %d", called, pid)
	}
	if resp.Msg.Status != "executed" {
		t.Fatalf("status=%q want executed", resp.Msg.Status)
	}
	// ②裁决者审计行锚（alerts 有「裁决者=dashboard RPC」行=与 feishu go 审计分辨）
	rows, err := s.Pool().Query(ctx, `SELECT message FROM alerts WHERE message LIKE '提案裁决审计%' ORDER BY ts DESC LIMIT 1`)
	if err != nil {
		t.Fatalf("alerts query: %v", err)
	}
	var got string
	for rows.Next() {
		_ = rows.Scan(&got)
	}
	rows.Close()
	if !strings.Contains(got, "裁决者=dashboard RPC") {
		t.Fatalf("裁决者审计行缺/不符：%q", got)
	}
	// ③幂等锚——非 proposed 行返拒不调径
	pid2, _, err := s.CreateProposal(ctx, &pgstore.Proposal{TemplateID: "tapprove_2", ExecTrack: "manual", Venue: "binance", Status: "proposed"})
	if err != nil {
		t.Fatalf("create2: %v", err)
	}
	if err := s.SetProposalStatus(ctx, pid2, "executed"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	called = -1
	resp2, err := svc.AckProposal(ctx, connect.NewRequest(&dashv1.AckProposalRequest{ProposalId: pid2, Action: "approve"}))
	if err != nil {
		t.Fatalf("approve2: %v", err)
	}
	if called != -1 {
		t.Fatalf("已裁行仍调径：called=%d", called)
	}
	if !strings.Contains(resp2.Msg.Message, "提案已定") {
		t.Fatalf("幂等返拒 message=%q", resp2.Msg.Message)
	}
}
