// service_appconfig_test.go —— D-523 锚测（A5 fake kill；A1-A4 在 appconfig_rpc_test.go 真 DSN 面）。
package dashboard

import (
	"context"
	"syscall"
	"testing"
	"time"

	dashv1 "nox/internal/gen/dashboardv1"

	"connectrpc.com/connect"
)

// A5：fake kill 注入断言 SIGTERM+~2s 延迟（不真杀测试进程）。
func TestD523_SelfRestartFakeKill(t *testing.T) {
	got := make(chan syscall.Signal, 1)
	svc := &Service{}
	svc.SetSelfRestart(func(pid int, sig syscall.Signal) error {
		if pid != syscall.Getpid() {
			t.Errorf("pid=%d want %d", pid, syscall.Getpid())
		}
		got <- sig
		return nil
	}, 50*time.Millisecond)
	resp, err := svc.SelfRestart(context.Background(), connect.NewRequest(&dashv1.SelfRestartRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	select {
	case sig := <-got:
		if sig != syscall.SIGTERM {
			t.Fatalf("sig=%v want SIGTERM", sig)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("kill not invoked within 3s")
	}
}
