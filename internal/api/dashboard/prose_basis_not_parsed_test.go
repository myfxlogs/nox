// D-176 锚：①ListProbeProposals 契约九字段+gate_note+proposed_at 读面；
// ③机审只读结构化 premise——basis 散文「段 XTZUSDT」段落标签不再被当 canonical 吞吃（#20 假歧义实证）。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

// #20 假歧义实证回归：basis 散文含「段 XTZUSDT」（LLM 段落标签）但结构化
// premise canonical=契约 CAPUSDT → 判据按 premise 走，不误判歧义（此例应硬伤拒=化石）。
func TestD176_ProseBasisNotParsed(t *testing.T) {
	svc, st := d163Svc(t)
	now := time.Now()
	d163Funding(t, st, "tbinance", 10, now)
	d163Funding(t, st, "okx", 137, now)
	id, ok, err := st.ExplorerProbeProposalInsert(context.Background(), pgstore.ProbeContract{
		Purpose: "p", Falsify: "1>0", VenueKey: "tbinance:perp", Canonical: "CAPUSDT",
		Side: "BUY", Qty: 1, NotionalUSD: 100, ExpectedCostUSD: 5,
		AutoCloseAt: now.Add(2 * time.Hour),
		// 散文 basis 含会致歧义的裸词——机审不再解析，应零影响。
		Basis: "term_dispersion 段 XTZUSDT funding min=10 max=137；idle_funds 段 okx free=100",
		Premises: []pgstore.ProbePremise{
			{Kind: "term_dispersion", Canonical: "CAPUSDT"},
			{Kind: "idle_funds", Venue: "okx", FreeUSD: 100},
		},
	}, now)
	if err != nil || !ok {
		t.Fatalf("insert: ok=%v err=%v", ok, err)
	}
	svc.probeOpener = func(context.Context, *v1.OpenProbeRequest) (*v1.OpenProbeResponse, error) {
		return &v1.OpenProbeResponse{Status: "OPEN", PositionId: 1}, nil
	}
	svc.adjudicateProbes(context.Background())
	status, note := d163State(t, st, id)
	// idle_funds 快照缺=歧义留决（venue 快照不可得于测试环境）；
	// 关键是「段 XTZUSDT」未触发 canonical 不等歧义。
	if status != "proposed" {
		t.Fatalf("快照歧义应留决 proposed, got %s note=%q", status, note)
	}
	if strings.Contains(note, "XTZUSDT") || strings.Contains(note, "≠") {
		t.Fatalf("散文裸词不得入判据, note=%q", note)
	}
	// D-206②：缺+回读败分文案——「轮询败」替代旧「权益快照缺」。
	if !strings.Contains(note, "轮询败") {
		t.Fatalf("idle_funds 应走 venue 轮询败歧义, note=%q", note)
	}
}

// ListProbeProposals：proposed 面字段全透传 + gate_note + proposed_at。
func TestD176_ListProbeProposals(t *testing.T) {
	svc, st := d163Svc(t)
	now := time.Now()
	id, ok, err := st.ExplorerProbeProposalInsert(context.Background(), pgstore.ProbeContract{
		Purpose: "验证 swap 实收", Falsify: "|swap|>0.01", VenueKey: "tbinance:perp", Canonical: "CAPUSDT",
		Side: "BUY", Qty: 0.5, NotionalUSD: 300, ExpectedCostUSD: 1.2,
		AutoCloseAt: now.Add(2 * time.Hour), Basis: "term_dispersion CAPUSDT",
		Premises: []pgstore.ProbePremise{{Kind: "term_dispersion", Canonical: "CAPUSDT"}},
	}, now)
	if err != nil || !ok {
		t.Fatalf("insert: ok=%v err=%v", ok, err)
	}
	if err := st.ProbeProposalGateNote(context.Background(), id, "机审歧义注记"); err != nil {
		t.Fatalf("gate note: %v", err)
	}
	resp, err := svc.ListProbeProposals(context.Background(), connect.NewRequest(&v1.ListProbeProposalsRequest{}))
	if err != nil {
		t.Fatalf("ListProbeProposals: %v", err)
	}
	if len(resp.Msg.GetProposals()) != 1 {
		t.Fatalf("n=%d, want 1", len(resp.Msg.GetProposals()))
	}
	c := resp.Msg.GetProposals()[0]
	if c.GetProposalId() != id || c.GetPurpose() != "验证 swap 实收" || c.GetVenueKey() != "tbinance:perp" ||
		c.GetCanonical() != "CAPUSDT" || c.GetSide() != "BUY" || c.GetQty() != 0.5 ||
		c.GetNotionalUsd() != 300 || c.GetExpectedCostUsd() != 1.2 ||
		c.GetAutoCloseAt() == nil || c.GetBasis() == "" ||
		c.GetGateNote() != "机审歧义注记" || c.GetProposedAt() == nil {
		t.Fatalf("字段透传不全: %+v", c)
	}
}
