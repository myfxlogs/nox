package dashboard

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	v1 "nox/internal/gen/dashboardv1"
)

// 视窗 C 件②锚：enabled presence——不携 enabled 建规则=默认启用（修复
// proto3 裸 bool 缺席=false 生来死规则病灶）；显式 false 才可关。
func TestWatchRuleEnabledPresence(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	svc := &Service{store: st}
	if _, err := st.Pool().Exec(ctx, `DELETE FROM watch_rule WHERE subject='ZZZUSDT'`); err != nil {
		t.Fatal(err)
	}
	mk := func(en *bool) *v1.WatchRule {
		return &v1.WatchRule{
			Kind: "price", Subject: "ZZZUSDT", Op: "gt", Threshold: 1,
			FireMode: "once", Enabled: en,
		}
	}
	// 缺席 → enabled=true
	if _, err := svc.UpsertWatchRule(ctx, connect.NewRequest(&v1.UpsertWatchRuleRequest{Rule: mk(nil)})); err != nil {
		t.Fatal(err)
	}
	// 显式 false → disabled
	f := false
	if _, err := svc.UpsertWatchRule(ctx, connect.NewRequest(&v1.UpsertWatchRuleRequest{Rule: mk(&f)})); err != nil {
		t.Fatal(err)
	}
	rows, err := svc.store.ListWatchRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []bool
	for _, r := range rows {
		if r.Subject == "ZZZUSDT" {
			got = append(got, r.Enabled)
		}
	}
	if len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("缺席应默认启用/显式 false 应停用: %v", got)
	}
}
