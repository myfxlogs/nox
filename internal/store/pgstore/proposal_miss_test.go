package pgstore

import (
	"context"
	"testing"
)

// TestProposalMissSticky expire 粘性修锚：单拍 miss 存活/连 N 拍杀/恢复清零。
func TestProposalMissSticky(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	// 种三提案：两 venue（live/dead）
	id1, _, err := st.CreateProposal(ctx, &Proposal{TemplateID: "cash_mgmt", Venue: "okx", ExecTrack: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	id2, _, err := st.CreateProposal(ctx, &Proposal{TemplateID: "cash_mgmt", Venue: "gate", ExecTrack: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	thr := 3
	// 单拍 miss（okx live 保、gate miss 1/3）——gate 提案存活
	b, e, err := st.BumpProposalMissOutsideVenues(ctx, "cash_mgmt", []string{"okx"}, thr)
	if err != nil || e != 0 {
		t.Fatalf("拍1应零杀，实 bump=%d expired=%d err=%v", b, e, err)
	}
	// gate 连 2/3、3/3 拍 miss——第 3 拍判死
	for i := 0; i < 2; i++ {
		if _, _, err = st.BumpProposalMissOutsideVenues(ctx, "cash_mgmt", []string{"okx"}, thr); err != nil {
			t.Fatal(err)
		}
	}
	// okx 行应还活（live 保）；gate 应已 expired
	var st1, st2 string
	_ = st.Pool().QueryRow(ctx, `SELECT status FROM fin_proposal WHERE id=$1`, id1).Scan(&st1)
	_ = st.Pool().QueryRow(ctx, `SELECT status FROM fin_proposal WHERE id=$1`, id2).Scan(&st2)
	if st1 != "proposed" || st2 != "expired" {
		t.Fatalf("okx=%s（应 proposed）gate=%s（应 expired）", st1, st2)
	}
}
