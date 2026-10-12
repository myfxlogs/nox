// proposal_test.go —— D-189 提案面锚测：稳定 id 幂等/过期并入扫描拍/最新 pending。
package pgstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"arb/internal/testutil"
)

func TestD189_ProposalLifecycle(t *testing.T) {
	st, err := New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `DELETE FROM fin_proposal`); err != nil {
		t.Fatal(err)
	}
	apr := 0.08
	// ①同模板 proposed 单行幂等——稳定 id 复用不重开。
	id1, c1, err := st.CreateProposal(ctx, &Proposal{
		TemplateID: "dci_sell_high_covered_call", ExecTrack: "auto",
		Venue: "binance", Params: map[string]any{"strike": 120000}, ExpectedAPR: &apr})
	if err != nil || !c1 || id1 <= 0 {
		t.Fatalf("首建败: %v c=%v id=%d", err, c1, id1)
	}
	id2, c2, err := st.CreateProposal(ctx, &Proposal{
		TemplateID: "dci_sell_high_covered_call", ExecTrack: "auto",
		Venue: "binance", ExpectedAPR: &apr})
	if err != nil || c2 || id2 != id1 {
		t.Fatalf("同模板 proposed 应复用 id: %v c=%v id=%d want %d", err, c2, id2, id1)
	}
	// ②不同模板可并存。
	idB, _, err := st.CreateProposal(ctx, &Proposal{
		TemplateID: "dci_buy_low", ExecTrack: "auto", Venue: "binance",
		Params: map[string]any{"product_ref": "P1"}})
	if err != nil || idB == id1 {
		t.Fatalf("异模板建行败: %v id=%d", err, idB)
	}
	// ③LatestPending=最新行。
	p, err := st.LatestPendingProposal(ctx, []string{"dci_sell_high_covered_call", "dci_buy_low"})
	if err != nil || p == nil || p.ID != idB {
		t.Fatalf("latest: %v %+v", err, p)
	}
	// ④decline 后同模板可再开新行（partial unique 锚）。
	if err := st.SetProposalStatus(ctx, id1, "declined"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetProposal(ctx, id1)
	if got.Status != "declined" || got.DecidedAt == nil {
		t.Fatalf("declined 态/decided_at 缺: %+v", got)
	}
	id3, c3, err := st.CreateProposal(ctx, &Proposal{
		TemplateID: "dci_sell_high_covered_call", ExecTrack: "auto", Venue: "binance"})
	if err != nil || !c3 || id3 == id1 {
		t.Fatalf("decline 后再开应新行: %v c=%v id=%d", err, c3, id3)
	}
	// ⑤过期并入扫描拍——不在 live 集者→expired，已定者不动。
	n, err := st.ExpireProposalsExcept(ctx, []string{"dci_buy_low"})
	if err != nil || n != 1 {
		t.Fatalf("expire n=%d want 1: %v", n, err)
	}
	got, _ = st.GetProposal(ctx, id3)
	if got.Status != "expired" || got.DecidedAt == nil {
		t.Fatalf("expired 态缺: %+v", got)
	}
	// ⑥二扫幂等零重扫（live 集不变→已定行不复写、buy_low 仍在集不动）。
	if n, err := st.ExpireProposalsExcept(ctx, []string{"dci_buy_low"}); err != nil || n != 0 {
		t.Fatalf("二轮 expire 应 0: %d %v", n, err)
	}
	// ⑦订单回链 proposal_id。
	td := 7
	oid, _, err := st.CreateDCIOrder(ctx, &DCIOrder{
		RequestID: fmt.Sprintf("t189|%d", time.Now().UnixNano()), Venue: "binance", ProductRef: "P1",
		OptType: "PUT", InvestCcy: "USDT", Qty: 1, TenorDays: &td,
		ProposalID: &idB})
	if err != nil || oid <= 0 {
		t.Fatalf("order proposal_id: %v id=%d", err, oid)
	}
	var pid int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT proposal_id FROM fin_dci_order WHERE id=$1`, oid).Scan(&pid); err != nil || pid != idB {
		t.Fatalf("回链缺失: %v pid=%d", err, pid)
	}
}
