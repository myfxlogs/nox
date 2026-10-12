// service_allowrules_d444_test.go —— D-444 T2：SetAutotradeAllowRuleEnabled RPC
// 翻转+note→alerts 表新增 info 行含 id/value/新态；不存在 id→NotFound。需 ARB_TEST_PG_DSN。
package dashboard

import (
	"context"
	"strings"
	"testing"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/platform/testutil/storetest"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

func d444Store(t *testing.T) *pgstore.Store {
	t.Helper()
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	// D-512 T7 连带：双 DELETE 拆两条（共享池 prepared 复用上报 08P01 偶发）。
	if _, err := st.Pool().Exec(context.Background(),
		`DELETE FROM autotrade_allow_rules WHERE value LIKE 'd444svc%'`); err != nil {
		t.Fatalf("clean rules: %v", err)
	}
	if _, err := st.Pool().Exec(context.Background(),
		`DELETE FROM alerts WHERE message LIKE '%d444svc%'`); err != nil {
		t.Fatalf("clean alerts: %v", err)
	}
	return st
}

func TestD444SetAllowRuleRPC(t *testing.T) {
	st := d444Store(t)
	svc := &Service{store: st}
	ctx := context.Background()

	var id int64
	if err := st.Pool().QueryRow(ctx, `
		INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated)
		VALUES ('venue','d444svc:mt5',true,false) RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 列表 RPC 含该行
	list, err := svc.ListAutotradeAllowRules(ctx, connect.NewRequest(&v1.ListAutotradeAllowRulesRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, r := range list.Msg.Rules {
		if r.Id == id && r.Enabled && r.Value == "d444svc:mt5" {
			found = true
		}
	}
	if !found {
		t.Fatal("seeded rule not in list")
	}

	// 翻转 → ok + info 审计行含 id/value/新态
	resp, err := svc.SetAutotradeAllowRuleEnabled(ctx, connect.NewRequest(&v1.SetAutotradeAllowRuleEnabledRequest{
		Id: id, Enabled: false, Note: "owner flip",
	}))
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("set: %v ok=%v", err, resp.Msg.GetOk())
	}
	var cnt int
	if err := st.Pool().QueryRow(ctx,
		`SELECT COUNT(*)::int FROM alerts WHERE level='info' AND message LIKE '%d444svc%' AND message LIKE '%enabled=false%'`).Scan(&cnt); err != nil {
		t.Fatalf("alerts: %v", err)
	}
	if cnt != 1 {
		t.Fatalf("audit alerts=%d want 1", cnt)
	}
	// DB 态回读
	var enabled bool
	if err := st.Pool().QueryRow(ctx,
		`SELECT enabled FROM autotrade_allow_rules WHERE id=$1`, id).Scan(&enabled); err != nil || enabled {
		t.Fatalf("enabled=%v err=%v", enabled, err)
	}
	// 不存在 id → NotFound
	_, err = svc.SetAutotradeAllowRuleEnabled(ctx, connect.NewRequest(&v1.SetAutotradeAllowRuleEnabledRequest{Id: 99999999}))
	if err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("missing id err=%v", err)
	}
}
