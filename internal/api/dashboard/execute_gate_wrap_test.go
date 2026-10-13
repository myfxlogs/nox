package dashboard

import (
	"context"
	"strings"
	"testing"

	"nox/internal/risk/gate"

	v1 "nox/internal/gen/dashboardv1"

	"connectrpc.com/connect"
)

// fakeOrderGate 闸假件——记录 Submit 调用，按预设返回；executeCalled 记录回调触达否。
type fakeOrderGate struct {
	res           gate.GateResult
	calls         int
	executeCalled bool
	runExecute    bool
}

func (f *fakeOrderGate) Submit(ctx context.Context, intent *gate.TradeIntent) gate.GateResult {
	f.calls++
	if f.runExecute {
		_, _, _ = intent.Execute(ctx)
		f.executeCalled = true
	}
	return f.res
}

// TestM43_ManualRPC_GateWrap D-262 M4.3① 钦定锚：手动执行面收口——
// ① 闸拒单可见（REJECTED+reason 透传）且 intent.Execute 未触达（零下单）；
// ② 闸放行才执行体（回调触达=正规径走通）；
// ③ 闸未接线=fail-closed FAILED（闸门缺省比绕闸安全）。
func TestM43_ManualRPC_GateWrap(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	svc, oppID := d328SeedManualFixture(t, st)
	ctx := context.Background()

	fg := &fakeOrderGate{res: gate.GateResult{Status: "REJECTED", Reason: "breaker reject-signature"}}
	svc.SetOrderGate(fg)
	resp, err := svc.ExecuteOpportunity(ctx, connect.NewRequest(&v1.ExecuteOpportunityRequest{
		OpportunityId: oppID, RequestId: "m43-t1", Qty: 1.0,
	}))
	if err != nil {
		t.Fatalf("rpc err: %v", err)
	}
	if fg.calls != 1 {
		t.Fatalf("gate 未被调用: calls=%d", fg.calls)
	}
	if resp.Msg.GetStatus() != "REJECTED" || !strings.Contains(resp.Msg.GetMessage(), "breaker reject-signature") {
		t.Fatalf("闸拒单不可见: %s %s", resp.Msg.GetStatus(), resp.Msg.GetMessage())
	}
	if fg.executeCalled {
		t.Fatal("闸拒单后 intent.Execute 竟被触达")
	}

	fg2 := &fakeOrderGate{res: gate.GateResult{Status: "EXECUTED", Reason: "ok"}, runExecute: true}
	svc.SetOrderGate(fg2)
	resp, err = svc.ExecuteOpportunity(ctx, connect.NewRequest(&v1.ExecuteOpportunityRequest{
		OpportunityId: oppID, RequestId: "m43-t2", Qty: 1.0,
	}))
	if err != nil || !fg2.executeCalled {
		t.Fatalf("放行径未触达执行体: err=%v called=%v", err, fg2.executeCalled)
	}
	if resp.Msg.GetStatus() != "EXECUTED" {
		t.Fatalf("闸放行结果未透传: %s", resp.Msg.GetStatus())
	}

	svc.SetOrderGate(nil)
	resp, err = svc.ExecuteOpportunity(ctx, connect.NewRequest(&v1.ExecuteOpportunityRequest{
		OpportunityId: oppID, RequestId: "m43-t3", Qty: 1.0,
	}))
	if err != nil {
		t.Fatalf("rpc err: %v", err)
	}
	if resp.Msg.GetStatus() != "FAILED" || !strings.Contains(resp.Msg.GetMessage(), "风控闸") {
		t.Fatalf("闸未接线应 fail-closed FAILED: %s %s", resp.Msg.GetStatus(), resp.Msg.GetMessage())
	}
}
