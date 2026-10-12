// brokers_symbolmap_d430_test.go —— D-430 T6：CanonicalByBrokerSymbol 命中/未命中/err 三态。
package pgstore

import (
	"context"
	"testing"
)

func TestD430_CanonicalByBrokerSymbol(t *testing.T) {
	s := d383MapStore(t)
	ctx := context.Background()
	d383MapSeed(t, s, "3001157", "XAUUSDm", "XAUUSD", true)

	c, ok, err := s.CanonicalByBrokerSymbol(ctx, "3001157", "XAUUSDm")
	if err != nil || !ok || c != "XAUUSD" {
		t.Fatalf("命中 = (%s,%v,%v), want (XAUUSD,true,nil)", c, ok, err)
	}
	if _, ok, err := s.CanonicalByBrokerSymbol(ctx, "3001157", "NOPEm"); err != nil || ok {
		t.Fatalf("未命中 = (%v,%v), want (false,nil) 非 error", ok, err)
	}
	// err 态：ctx 取消 → error 非 ok=false
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, ok, err := s.CanonicalByBrokerSymbol(cancelCtx, "3001157", "XAUUSDm"); err == nil {
		t.Fatalf("ctx 取消 err=%v ok=%v, want error", err, ok)
	}
}
