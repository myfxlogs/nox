// service_allowrules_d504_test.go —— D-504 S2 真库锚：venue 规则指向
// broker_accounts.is_real=false 户 → 管理面 List 不列；real 户/canonical 规则保留；
// is_real 未知 venue 保守列出。需 NOX_TEST_PG_DSN。
package dashboard

import (
	"context"
	"testing"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/platform/testutil/storetest"

	"connectrpc.com/connect"
)

func TestD504_AllowRulesDemoFilter(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `
		DELETE FROM autotrade_allow_rules WHERE value LIKE 'd504%';
		DELETE FROM broker_accounts WHERE venue LIKE 'd504%'`); err != nil {
		t.Fatalf("clean: %v", err)
	}
	// 种子：demo 户/real 户/无账号 venue 三类 + canonical 规则
	if _, err := st.Pool().Exec(ctx, `
		INSERT INTO broker_accounts (venue, venue_type, host, port, password, is_real)
			VALUES ('d504demo','mt5','h',443,'p',false), ('d504real','mt5','h',443,'p',true);
		INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated, note) VALUES
			('venue','d504demo:mt5',true,false,'t'),
			('venue','d504real:mt5',true,false,'t'),
			('venue','d504none:mt5',true,false,'t'),
			('canonical','D504C',true,false,'t')
		ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `
			DELETE FROM autotrade_allow_rules WHERE value LIKE 'd504%';
			DELETE FROM broker_accounts WHERE venue LIKE 'd504%'`)
	})
	s := &Service{store: st}
	resp, err := s.ListAutotradeAllowRules(ctx, connect.NewRequest(&v1.ListAutotradeAllowRulesRequest{}))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]bool{}
	for _, r := range resp.Msg.Rules {
		got[r.Value] = true
	}
	if got["d504demo:mt5"] {
		t.Error("is_real=f 户 venue 规则应滤除")
	}
	for _, want := range []string{"d504real:mt5", "d504none:mt5", "D504C"} {
		if !got[want] {
			t.Errorf("%s 应保留", want)
		}
	}
}
