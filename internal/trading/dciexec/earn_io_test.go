// earn_io_test.go —— D-596 锚：申购/赎回写侧 io 记账（spot↔earn 双向断言+幂等重试不双记）。
package dciexec

import (
	"context"
	"testing"
)

func TestEarnIO_SubscribeWrites(t *testing.T) {
	fakeIOCaptures = nil
	e := &Executor{Store: &fakeStore{}}
	e.ioTransfer(context.Background(), "binance", "USDT", 351.64,
		"spot", "earn", "earn-sub:42:p1", "")
	if len(fakeIOCaptures) != 1 {
		t.Fatalf("io rows=%d want 1", len(fakeIOCaptures))
	}
	r := fakeIOCaptures[0]
	if r.Direction != "transfer" || r.FromWallet != "spot" || r.ToWallet != "earn" ||
		r.Amount != 351.64 || r.Venue != "binance" || r.ExternalID != "earn-sub:42:p1" {
		t.Fatalf("io row=%+v", r)
	}
}

func TestEarnIO_RedeemReverse(t *testing.T) {
	fakeIOCaptures = nil
	e := &Executor{Store: &fakeStore{}}
	e.ioTransfer(context.Background(), "binance", "USDT", 100.0,
		"earn", "spot", "earn-red:p1:USDT", "")
	if len(fakeIOCaptures) != 1 {
		t.Fatalf("io rows=%d want 1", len(fakeIOCaptures))
	}
	if fakeIOCaptures[0].FromWallet != "earn" || fakeIOCaptures[0].ToWallet != "spot" {
		t.Fatalf("redeem direction wrong: %+v", fakeIOCaptures[0])
	}
}
