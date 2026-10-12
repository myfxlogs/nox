// d200_test.go —— D-200②：ListDCIOrders 看板读面（id DESC/proposal 链/response≤300 截）。
package pgstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// D-200②锚：id DESC 序+proposal_id 回链+response 截≤300 字符+limit 生效。
func TestD200_ListDCIOrders(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if _, err := st.pool.Exec(ctx, `DELETE FROM fin_dci_order; DELETE FROM fin_proposal`); err != nil {
		t.Fatalf("清面: %v", err)
	}
	if _, err := st.pool.Exec(ctx,
		`INSERT INTO fin_proposal (id, template_id, venue, exec_track, params, expected_apr, status)
		 VALUES (7, 'dci_buy_low', 'binance', 'dci', '{}'::jsonb, 0.1, 'executed')`); err != nil {
		t.Fatalf("建提案: %v", err)
	}
	long := strings.Repeat("x", 400)
	qty := 541.0
	rows := []struct {
		req, resp string
		prop      *int64
	}{
		{"r10|p", "ok", nil},
		{"r11|p", long, ptr64(7)},
		{"r12|p", "fail-body", ptr64(7)},
	}
	for _, r := range rows {
		id, ins, err := st.CreateDCIOrder(ctx, &DCIOrder{
			RequestID: r.req, Venue: "binance", TemplateID: "dci_buy_low",
			ProductRef: "P1", Underlying: "BTC", OptType: "P", InvestCcy: "USDT",
			Qty: qty, ProposalID: r.prop,
		})
		if err != nil || !ins {
			t.Fatalf("建单: ins=%v err=%v", ins, err)
		}
		if err := st.FinishDCIOrder(ctx, id, "failed", r.resp); err != nil {
			t.Fatalf("收口: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	list, err := st.ListDCIOrders(ctx, 2)
	if err != nil {
		t.Fatalf("ListDCIOrders: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("limit 行数 %d≠2", len(list))
	}
	if list[0].RequestID != "r12|p" || list[1].RequestID != "r11|p" {
		t.Fatalf("非 id DESC：%s,%s", list[0].RequestID, list[1].RequestID)
	}
	if list[1].ProposalID == nil || *list[1].ProposalID != 7 {
		t.Fatalf("proposal 链断：%v", list[1].ProposalID)
	}
	if len(list[1].Response) != 300 {
		t.Fatalf("response 未截 300：len=%d", len(list[1].Response))
	}
	if list[0].CreatedAt.IsZero() {
		t.Fatalf("created_at 缺")
	}
}

func ptr64(v int64) *int64 { return &v }
