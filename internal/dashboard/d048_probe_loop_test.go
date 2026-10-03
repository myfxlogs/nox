// d048_probe_loop_test.go —— D-048②③：探针提案审批链 + 平仓报告触发锚。
// 全路径禁真单：probeOpener fake 计数（D-047 OpenProbe 执行面替身）。
package dashboard

import (
	"context"
	"fmt"
	"testing"
	"time"

	"arb/internal/exec"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"

	"connectrpc.com/connect"
)

// d048Proposal 造一条合法探针提案行（返回 proposal_id）。
func d048Proposal(t *testing.T, st *pgstore.Store) int64 {
	t.Helper()
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE explorer_proposals`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	id, ok, err := st.ExplorerProbeProposalInsert(ctx, pgstore.ProbeContract{
		Purpose: "验证 UKOIL swap_free 是否真实免息", Falsify: "持仓 24h 内 |swap 实收| > 0.01 USD",
		VenueKey: "tbinance:perp", Canonical: "BTCUSDT", Side: "BUY", Qty: 0.005,
		NotionalUSD: 300, ExpectedCostUSD: 1.5, AutoCloseAt: time.Now().Add(2 * time.Hour),
		Basis: "term_dispersion BTCUSDT binance×gate",
	}, time.Now())
	if err != nil || !ok || id <= 0 {
		t.Fatalf("insert proposal: id=%d ok=%v err=%v", id, ok, err)
	}
	return id
}

func d048ProposalStatus(t *testing.T, st *pgstore.Store, id int64) (string, *int64, string) {
	t.Helper()
	var status, ref string
	var posID *int64
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT status, COALESCE(decision_ref,''), probe_position_id FROM explorer_proposals WHERE id=$1`, id).
		Scan(&status, &ref, &posID); err != nil {
		t.Fatalf("proposal status: %v", err)
	}
	return status, posID, ref
}

// TestD048_ApproveProbe_OpensOnce 批准 → probeOpener 恰调一次 → running+关联仓；
// 重放 approve → 不再调（CAS 占单）。
func TestD048_ApproveProbe_OpensOnce(t *testing.T) {
	svc, st, _ := d047Svc(t)
	pid := d048Proposal(t, st)
	calls := 0
	svc.probeOpener = func(_ context.Context, r *v1.OpenProbeRequest) (*v1.OpenProbeResponse, error) {
		calls++
		if r.GetVenueKey() != "tbinance:perp" || r.GetCanonical() != "BTCUSDT" ||
			r.GetSide() != "BUY" || r.GetQty() != 0.005 || r.GetDecisionRef() != "D-048" {
			return nil, fmt.Errorf("open req fields: %+v", r)
		}
		return &v1.OpenProbeResponse{Status: "OPEN", PositionId: 777}, nil
	}
	req := connect.NewRequest(&v1.ApproveProbeRequest{ProposalId: pid, DecisionRef: "D-048"})
	resp, err := svc.ApproveProbe(context.Background(), req)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if calls != 1 || resp.Msg.GetStatus() != "RUNNING" || resp.Msg.GetPositionId() != 777 {
		t.Fatalf("calls=%d resp=%+v", calls, resp.Msg)
	}
	status, posID, ref := d048ProposalStatus(t, st, pid)
	if status != "running" || posID == nil || *posID != 777 || ref != "D-048" {
		t.Fatalf("proposal: status=%s pos=%v ref=%s", status, posID, ref)
	}
	// 幂等重放：状态已 running → 返回当前态不重放执行。
	resp2, err := svc.ApproveProbe(context.Background(), req)
	if err != nil || calls != 1 {
		t.Fatalf("replay: calls=%d err=%v", calls, err)
	}
	if resp2.Msg.GetStatus() != "running" {
		t.Fatalf("replay status=%s", resp2.Msg.GetStatus())
	}
}

// TestD048_ApproveProbe_OpenProbeRejects OpenProbe 拒 → 回 proposed + warn 告警。
func TestD048_ApproveProbe_OpenProbeRejects(t *testing.T) {
	svc, st, _ := d047Svc(t)
	pid := d048Proposal(t, st)
	svc.probeOpener = func(_ context.Context, _ *v1.OpenProbeRequest) (*v1.OpenProbeResponse, error) {
		return &v1.OpenProbeResponse{Status: "FAILED", Message: "探针通道未启用"}, nil
	}
	resp, err := svc.ApproveProbe(context.Background(),
		connect.NewRequest(&v1.ApproveProbeRequest{ProposalId: pid, DecisionRef: "D-048"}))
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if resp.Msg.GetStatus() != "PROPOSED" {
		t.Fatalf("status=%s", resp.Msg.GetStatus())
	}
	status, _, _ := d048ProposalStatus(t, st, pid)
	if status != "proposed" {
		t.Fatalf("proposal status=%s, want proposed", status)
	}
	var warns int
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT COUNT(*) FROM alerts WHERE level='warn' AND message LIKE '%提案%OpenProbe 拒%'`).Scan(&warns); err != nil {
		t.Fatalf("warns: %v", err)
	}
	if warns == 0 {
		t.Error("OpenProbe 拒绝未落 warn 告警")
	}
}

// TestD048_RejectProbe_NoOrder 拒绝 → 绝不动执行面；status=rejected + 理由落库。
func TestD048_RejectProbe_NoOrder(t *testing.T) {
	svc, st, fx := d047Svc(t)
	pid := d048Proposal(t, st)
	calls := 0
	svc.probeOpener = func(_ context.Context, _ *v1.OpenProbeRequest) (*v1.OpenProbeResponse, error) {
		calls++
		return &v1.OpenProbeResponse{Status: "OPEN", PositionId: 1}, nil
	}
	resp, err := svc.RejectProbe(context.Background(),
		connect.NewRequest(&v1.RejectProbeRequest{ProposalId: pid, Reason: "预算不值"}))
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if resp.Msg.GetStatus() != "REJECTED" || calls != 0 || len(fx.places) != 0 {
		t.Fatalf("resp=%+v calls=%d places=%d", resp.Msg, calls, len(fx.places))
	}
	var gateNote string
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT gate_note FROM explorer_proposals WHERE id=$1`, pid).Scan(&gateNote); err != nil {
		t.Fatalf("gate note: %v", err)
	}
	if gateNote != "预算不值" {
		t.Fatalf("gate_note=%q", gateNote)
	}
	// rejected 态再 approve → 不重放执行。
	resp2, err := svc.ApproveProbe(context.Background(),
		connect.NewRequest(&v1.ApproveProbeRequest{ProposalId: pid, DecisionRef: "D-048"}))
	if err != nil || calls != 0 || resp2.Msg.GetStatus() != "rejected" {
		t.Fatalf("approve-after-reject: calls=%d err=%v resp=%+v", calls, err, resp2.Msg)
	}
}

// TestD048_ApproveProbe_BadInputs 非法输入锚：ref 形拒 + 非探针提案 NotFound。
func TestD048_ApproveProbe_BadInputs(t *testing.T) {
	svc, st, _ := d047Svc(t)
	pid := d048Proposal(t, st)
	if _, err := svc.ApproveProbe(context.Background(),
		connect.NewRequest(&v1.ApproveProbeRequest{ProposalId: pid, DecisionRef: "d48"})); err == nil {
		t.Error("bad decision_ref 未拒")
	}
	if _, err := svc.ApproveProbe(context.Background(),
		connect.NewRequest(&v1.ApproveProbeRequest{ProposalId: 99999, DecisionRef: "D-048"})); err == nil {
		t.Error("nonexistent proposal 未拒")
	}
	// daily 提案（scan_kind='daily'）不可走探针审批。
	if _, err := st.Pool().Exec(context.Background(),
		`INSERT INTO explorer_proposals (scan_date, scan_kind, canonical) VALUES (now(),'daily','BTCUSDT')`); err != nil {
		t.Fatalf("daily insert: %v", err)
	}
	var dailyID int64
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT id FROM explorer_proposals WHERE scan_kind='daily'`).Scan(&dailyID); err != nil {
		t.Fatalf("daily id: %v", err)
	}
	if _, err := svc.ApproveProbe(context.Background(),
		connect.NewRequest(&v1.ApproveProbeRequest{ProposalId: dailyID, DecisionRef: "D-048"})); err == nil {
		t.Error("daily proposal 走通探针审批——应 NotFound")
	}
}

// TestD048_ExpireClose_TriggersReportOnce 到期平仓 → 报告钩恰一次 + 提案 running→closed；
// 重跑 ExpireDueProbes 不再触发（仓已 CLOSED 不入 due 集）。
func TestD048_ExpireClose_TriggersReportOnce(t *testing.T) {
	svc, st, _ := d047Svc(t)
	ctx := context.Background()
	mt := &fakeProbeExec{key: "3001:mt5",
		closeFill: exec.Fill{OrderID: "c-1", Price: 2000.5, Qty: 0.01, FeeUSD: 0}}
	svc.bm.reg.Register(mt)
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password)
		 VALUES ('3001','mt5','h',443,'p') ON CONFLICT (venue) DO NOTHING;
		 INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, contract_size)
		 VALUES ('3001','XAUUSDm','XAUUSD',100) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("symbol map: %v", err)
	}
	past := time.Now().Add(-time.Minute)
	ref := "D-048"
	posID, err := st.CreatePosition(ctx, &pgstore.Position{
		OpportunityID: "PROBE:XAUUSD:" + ref, Type: pgstore.PositionTypePROBE,
		Status: "OPEN", DecisionRef: &ref, AutoCloseAt: &past})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	now := time.Now()
	openPx := 2000.0
	if err := st.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 1, Phase: "OPEN",
		Venue: "3001:mt5", Account: "3001", Symbol: "XAUUSDm", Side: "BUY", Qty: 0.01,
		PriceActual: &openPx, OrderID: "t-9", Status: "FILLED",
		AttemptNo: 1, FilledQty: &openPx, AvgFillPrice: &openPx, SubmittedAt: &now, Ts: now,
	}); err != nil {
		t.Fatalf("leg: %v", err)
	}
	// running 提案关联该仓（approve 已走通的状态）。
	pid := d048Proposal(t, st)
	if _, err := st.Pool().Exec(ctx,
		`UPDATE explorer_proposals SET status='running', probe_position_id=$2, decision_ref='D-048' WHERE id=$1`,
		pid, posID); err != nil {
		t.Fatalf("link: %v", err)
	}
	reported := 0
	var gotPos int64
	svc.probeReporter = func(_ context.Context, p int64) { reported++; gotPos = p }

	svc.ExpireDueProbes(ctx)
	svc.ExpireDueProbes(ctx) // 重扫——已 CLOSED 不再触发

	if reported != 1 || gotPos != posID {
		t.Fatalf("reported=%d pos=%d", reported, gotPos)
	}
	var status string
	if err := st.Pool().QueryRow(ctx,
		`SELECT status FROM explorer_proposals WHERE id=$1`, pid).Scan(&status); err != nil {
		t.Fatalf("proposal: %v", err)
	}
	if status != "closed" {
		t.Fatalf("proposal status=%s, want closed", status)
	}
}
