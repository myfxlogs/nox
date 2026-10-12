// appconfig_rpc_test.go —— D-523 A1-A4 锚测（真库 app_config 行 + Service 键视图）。
package dashboard

import (
	"context"
	"testing"
	"time"

	dashv1 "nox/internal/gen/dashboardv1"
	"nox/internal/platform/config"

	"connectrpc.com/connect"
)

func d523Svc(t *testing.T) *Service {
	t.Helper()
	s := serviceTestStoreSingleInit(t)
	svc := &Service{store: s}
	// running=false / defaults=false / 无 env——基线确定性构造。
	svc.SetAppConfigView(&config.Config{ExplorerEnabled: false}, &config.Config{ExplorerEnabled: false}, map[string]string{})
	return svc
}

func d523Find(resp *dashv1.ListAppConfigResponse, key string) *dashv1.AppConfigKey {
	for _, k := range resp.Keys {
		if k.Key == key {
			return k
		}
	}
	return nil
}

// A1：全量键视图 + valueSource（无 DB 行无 env → default）+ runningValue。
func TestD523_A1_ListKeyView(t *testing.T) {
	svc := d523Svc(t)
	ctx := context.Background()
	if _, err := svc.store.Pool().Exec(ctx, `DELETE FROM app_config WHERE key='NOX_EXPLORER_ENABLED'`); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.ListAppConfig(ctx, connect.NewRequest(&dashv1.ListAppConfigRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	k := d523Find(resp.Msg, "NOX_EXPLORER_ENABLED")
	if k == nil {
		t.Fatal("key missing")
	}
	if k.ValueSource != "default" || k.RunningValue != "false" || k.Type != "bool" || k.Group != "EXPLORER" || k.PendingRestart {
		t.Fatalf("bad view: %+v", k)
	}
}

// A2：非白名单键 / bool 值 "maybe" / note 空 → 各 InvalidArgument。
func TestD523_A2_UpdateValidation(t *testing.T) {
	svc := d523Svc(t)
	ctx := context.Background()
	cases := []*dashv1.UpdateAppConfigRequest{
		{Key: "NOX_NOPE", Value: "1", Note: "n"},
		{Key: "NOX_EXPLORER_ENABLED", Value: "maybe", Note: "n"},
		{Key: "NOX_EXPLORER_ENABLED", Value: "true", Note: ""},
	}
	for i, c := range cases {
		_, err := svc.UpdateAppConfig(ctx, connect.NewRequest(c))
		if ce := new(connect.Error); err == nil || !asConnect(err, ce) || ce.Code() != connect.CodeInvalidArgument {
			t.Fatalf("case %d: want InvalidArgument, got %v", i, err)
		}
	}
}

func asConnect(err error, ce *connect.Error) bool {
	if e, ok := err.(*connect.Error); ok {
		*ce = *e
		return true
	}
	return false
}

// A3：同键二调幂等——value/note/updated_at 更新。
func TestD523_A3_UpdateIdempotent(t *testing.T) {
	svc := d523Svc(t)
	ctx := context.Background()
	r1, err := svc.UpdateAppConfig(ctx, connect.NewRequest(&dashv1.UpdateAppConfigRequest{Key: "NOX_EXPLORER_ENABLED", Value: "true", Note: "a3 首调"}))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	r2, err := svc.UpdateAppConfig(ctx, connect.NewRequest(&dashv1.UpdateAppConfigRequest{Key: "NOX_EXPLORER_ENABLED", Value: "true", Note: "a3 再调"}))
	if err != nil {
		t.Fatal(err)
	}
	if r2.Msg.Key.Note != "a3 再调" || r2.Msg.Key.UpdatedAt.AsTime().Before(r1.Msg.Key.UpdatedAt.AsTime()) {
		t.Fatalf("note/updated_at not refreshed: %+v vs %+v", r1.Msg.Key, r2.Msg.Key)
	}
	// 清理（其他锚依赖无 DB 行基线）
	if _, err := svc.store.Pool().Exec(ctx, `DELETE FROM app_config WHERE key='NOX_EXPLORER_ENABLED'`); err != nil {
		t.Fatal(err)
	}
}

// A4 红锚：DB 生效值≠running → pendingRestart=true（不重启不清零）。
func TestD523_A4_PendingRestart(t *testing.T) {
	svc := d523Svc(t)
	ctx := context.Background()
	if _, err := svc.UpdateAppConfig(ctx, connect.NewRequest(&dashv1.UpdateAppConfigRequest{Key: "NOX_EXPLORER_ENABLED", Value: "true", Note: "a4"})); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.ListAppConfig(ctx, connect.NewRequest(&dashv1.ListAppConfigRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	k := d523Find(resp.Msg, "NOX_EXPLORER_ENABLED")
	if k == nil || !k.PendingRestart || k.ValueSource != "db" || k.RunningValue != "false" {
		t.Fatalf("want pendingRestart=true source=db running=false, got %+v", k)
	}
	if _, err := svc.store.Pool().Exec(ctx, `DELETE FROM app_config WHERE key='NOX_EXPLORER_ENABLED'`); err != nil {
		t.Fatal(err)
	}
}

// A2（D-526）：List 键条目带中文 desc + 中文组标签（冗余字段前端零映射）。
func TestD526_A2_DescAndGroupLabel(t *testing.T) {
	svc := d523Svc(t)
	resp, err := svc.ListAppConfig(context.Background(), connect.NewRequest(&dashv1.ListAppConfigRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	k := d523Find(resp.Msg, "NOX_EXPLORER_ENABLED")
	if k == nil {
		t.Fatal("key missing")
	}
	if k.Desc == "" || k.GroupLabel != "探矿" {
		t.Fatalf("want 中文 desc+探矿 label, got desc=%q label=%q", k.Desc, k.GroupLabel)
	}
}
