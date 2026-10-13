package dashboard

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

// TestM46_RequestBox 钦定验收锚（真实测试库）：
// requested 行→箱面可见+龄→ResolveRequest 翻态+note 留痕→出集幂等。
func TestM46_RequestBox(t *testing.T) {
	st := testServiceStore(t)
	ctx := context.Background()
	defer func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM fin_condition_gap WHERE template_id='m46_t'`)
	}()
	// capital kind 同态机种子（D-268②：capital 全程同态机端到端实证）
	id, err := st.OpenConditionGap(ctx, &pgstore.ConditionGap{
		TemplateID: "m46_t", Kind: "capital", Subject: "binance:perp",
		Qualifier: "min_usd", RequestText: "请向 binance perp 充 100 USDT",
	})
	if err != nil {
		t.Fatalf("open gap: %v", err)
	}
	if err := st.RequestConditionGap(ctx, id); err != nil {
		t.Fatalf("request gap: %v", err)
	}
	svc := &Service{store: st}

	// ① 箱面显行+龄
	resp, err := svc.ListRequestBox(ctx, connect.NewRequest(&v1.ListRequestBoxRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var it *v1.RequestBoxItem
	for _, i := range resp.Msg.Items {
		if i.TemplateId == "m46_t" {
			it = i
		}
	}
	if it == nil {
		t.Fatal("requested 行未入箱")
	}
	if it.Kind != "capital" || it.Subject != "binance:perp" || it.AgeSeconds < 0 {
		t.Fatalf("行面错: %+v", it)
	}

	// ② 非法态→invalid_argument
	if _, err := svc.ResolveRequest(ctx, connect.NewRequest(&v1.ResolveRequestRequest{
		Id: id, State: v1.ResolveRequestState_RESOLVE_REQUEST_STATE_UNSPECIFIED,
	})); err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("非法态应 invalid_argument: %v", err)
	}

	// ③ fulfilled 回执→applied+翻态+note
	rs, err := svc.ResolveRequest(ctx, connect.NewRequest(&v1.ResolveRequestRequest{
		Id: id, State: v1.ResolveRequestState_RESOLVE_REQUEST_STATE_FULFILLED,
	}))
	if err != nil || !rs.Msg.Applied {
		t.Fatalf("resolve: applied=%v err=%v", rs.Msg.Applied, err)
	}
	var status, note string
	if err := st.Pool().QueryRow(ctx,
		`SELECT status, COALESCE(resolve_note,'') FROM fin_condition_gap WHERE id=$1`, id).
		Scan(&status, &note); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "fulfilled" || note != "投资人页回执" {
		t.Fatalf("翻态/留痕错: %s %q", status, note)
	}

	// ④ 幂等：复按 applied=false + 出箱
	rs2, _ := svc.ResolveRequest(ctx, connect.NewRequest(&v1.ResolveRequestRequest{
		Id: id, State: v1.ResolveRequestState_RESOLVE_REQUEST_STATE_WAIVED,
	}))
	if rs2.Msg.Applied {
		t.Fatal("已收口行复按不应 applied")
	}
	resp2, _ := svc.ListRequestBox(ctx, connect.NewRequest(&v1.ListRequestBoxRequest{}))
	for _, i := range resp2.Msg.Items {
		if i.Id == id {
			t.Fatal("fulfilled 行滞留箱面")
		}
	}
}
