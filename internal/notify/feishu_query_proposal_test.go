// feishu_query_proposal_test.go —— D-194：「提案」口令锚。
package notify

import (
	"context"
	"strings"
	"testing"

	"arb/internal/store/pgstore"
)

type fakePropLister struct{ rows []pgstore.Proposal }

func (f *fakePropLister) ListProposals(_ context.Context, status string, limit int) ([]pgstore.Proposal, error) {
	if status != "proposed" || limit != 10 {
		return nil, nil
	}
	return f.rows, nil
}

func TestD194_ProposalCmd(t *testing.T) {
	if !isProposalCmd("提案") || !isProposalCmd(" 提案 ") || isProposalCmd("这个提案怎么样") || isProposalCmd("提案列表") {
		t.Fatal("口令形误判（长句含提案应落 LLM）")
	}
	h := &FeishuQueryHandler{}
	msg, _ := h.handleProposalCmd(context.Background())
	if !strings.Contains(msg, "未接线") {
		t.Fatal("未接线回执缺")
	}
	f := &fakePropLister{}
	h.SetProposalLister(f)
	msg, _ = h.handleProposalCmd(context.Background())
	if !strings.Contains(msg, "无 pending") {
		t.Fatalf("空集未如实报无: %q", msg)
	}
	f.rows = []pgstore.Proposal{
		{ID: 3, TemplateID: "dci_buy_low", Venue: "binance",
			Params: map[string]any{"product_ref": "PUTP", "strike": 82000.0, "apr": 0.09},
			Status: "proposed"},
		{ID: 5, TemplateID: "earn_apr_lending", Venue: "binance",
			Params: map[string]any{}, Status: "proposed"},
	}
	msg, _ = h.handleProposalCmd(context.Background())
	if !strings.Contains(msg, "#3 dci_buy_low|binance|") ||
		!strings.Contains(msg, "product_ref=PUTP") || !strings.Contains(msg, "strike=82000") ||
		!strings.Contains(msg, "#5 earn_apr_lending|binance|无快照") ||
		!strings.Contains(msg, "go N") || !strings.Contains(msg, "拒 N") {
		t.Fatalf("回执形错: %q", msg)
	}
}
