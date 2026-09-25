package dashboard

import (
	"context"
	"strings"
	"testing"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

// TestD470_AddBrokerRejectsMT4 T1：platform=0 新建 → InvalidArgument + 「已退役」字样；
// platform=1 过闸（后续 verifyMT5 拨号失败属预期离线错误=闸已放行证明）。
func TestD470_AddBrokerRejectsMT4(t *testing.T) {
	svc := d388Service(t)
	ctx := context.Background()

	// platform=0 → 明确拒绝
	_, err := svc.AddBroker(ctx, connect.NewRequest(&v1.AddBrokerRequest{
		Venue: "mt4acct", Platform: 0, Host: "h", User: 1, Password: "p", Server: "s",
	}))
	if err == nil {
		t.Fatal("platform=0 AddBroker should fail")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("platform=0 code=%v, want InvalidArgument", connect.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "已退役") {
		t.Fatalf("platform=0 error=%q, want 含「已退役」", err.Error())
	}

	// platform=1 → 过 platform 闸（verifyMT5 离线拨号失败=Unavailable 非 InvalidArgument）
	_, err = svc.AddBroker(ctx, connect.NewRequest(&v1.AddBrokerRequest{
		Venue: "mt5acct", Platform: 1, Host: "127.0.0.1:1", User: 1, Password: "p", Server: "s",
	}))
	if err == nil {
		t.Fatal("offline verifyMT5 should fail")
	}
	if connect.CodeOf(err) == connect.CodeInvalidArgument && strings.Contains(err.Error(), "已退役") {
		t.Fatalf("platform=1 wrongly rejected: %v", err)
	}
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("platform=1 code=%v, want Unavailable（platform 闸已放行，拨号失败）", connect.CodeOf(err))
	}
}

// TestD470_VerifyAccountRejectsMT4 T1b：VerifyAccount platform=0 同拒。
func TestD470_VerifyAccountRejectsMT4(t *testing.T) {
	svc := d388Service(t)
	_, err := svc.VerifyAccount(context.Background(), connect.NewRequest(&v1.VerifyAccountRequest{
		Platform: 0, Host: "h", Server: "s", User: 1, Password: "p",
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("VerifyAccount platform=0: err=%v, want InvalidArgument", err)
	}
	if !strings.Contains(err.Error(), "已退役") {
		t.Fatalf("VerifyAccount error=%q, want 含「已退役」", err.Error())
	}
}

// TestD470_StartAdapterRejectsMT4 T1c：存量 stopped 行被误启时 fail-closed（platform=0 → error）。
func TestD470_StartAdapterRejectsMT4(t *testing.T) {
	svc := d388Service(t)
	err := svc.bm.startAdapter(context.Background(), &pgstore.BrokerAccount{Venue: "old", Platform: 0})
	if err == nil || !strings.Contains(err.Error(), "已退役") {
		t.Fatalf("startAdapter platform=0: err=%v, want 含「已退役」", err)
	}
}
