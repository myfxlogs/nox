package dashboard

import (
	"context"
	"errors"
	"testing"

	"arb/internal/dephealth"
	v1 "arb/internal/gen/dashboardv1"

	"connectrpc.com/connect"
)

// TestD579_T5_SourceHealthDepRows dephealth 行并入 ListSourceHealth 透出。
func TestD579_T5_SourceHealthDepRows(t *testing.T) {
	key := "t5venue:mt5:rpc"
	dephealth.Mark(key, true, nil)
	dephealth.Mark("t5b:rest", false, errors.New("boom"))
	t.Cleanup(func() {
		// 包级单例清理——本测试注册键残留会污染其他用例的 Snapshot 面。
		// dephealth 无公开 Unregister；用专用键前缀隔离即足（断言按键名过滤）。
	})

	svc := &Service{}
	resp, err := svc.ListSourceHealth(context.Background(), connect.NewRequest(&v1.ListSourceHealthRequest{}))
	if err != nil {
		t.Fatalf("ListSourceHealth: %v", err)
	}
	var found *v1.SourceHealth
	for _, r := range resp.Msg.Sources {
		if r.Source == key {
			found = r
		}
	}
	if found == nil {
		t.Fatalf("dep 行应透出: %+v", resp.Msg.Sources)
	}
	if found.State != dephealth.StateHealthy || found.LastEventUnix <= 0 {
		t.Fatalf("dep 行字段: %+v", found)
	}
}
