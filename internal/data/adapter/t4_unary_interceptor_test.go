package adapter

import (
	"context"
	"errors"
	"testing"

	"arb/internal/infra/dephealth"

	"google.golang.org/grpc"
)

func depRowAdapter(key string) (dephealth.Row, bool) {
	for _, r := range dephealth.Snapshot() {
		if r.Key == key {
			return r, true
		}
	}
	return dephealth.Row{}, false
}

// TestD579_T4_UnaryInterceptor mt5 unary interceptor 成败记账+透传不改语义。
func TestD579_T4_UnaryInterceptor(t *testing.T) {
	key := "t4venue:mt5:rpc"
	it := depUnaryInterceptor(key)

	sentinel := errors.New("upstream gone")
	failInvoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		return sentinel
	}
	if err := it(context.Background(), "/m/Method", nil, nil, nil, failInvoker); !errors.Is(err, sentinel) {
		t.Fatalf("interceptor 须透传原 err: %v", err)
	}
	r, ok := depRowAdapter(key)
	if !ok || r.ConsecFails != 1 || r.State != dephealth.StateSuspect || r.LastErr != "upstream gone" {
		t.Fatalf("fail 应记账: %+v", r)
	}

	okInvoker := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		return nil
	}
	if err := it(context.Background(), "/m/Method", nil, nil, nil, okInvoker); err != nil {
		t.Fatalf("ok 应透传 nil: %v", err)
	}
	r, _ = depRowAdapter(key)
	if r.State != dephealth.StateHealthy || r.ConsecFails != 0 {
		t.Fatalf("ok 应 healthy 计数归位: %+v", r)
	}
}
