// earn_io_test.go —— earn-io-ledger 票锚：申购/赎回写侧 io 记账（spot↔earn 双向断言+幂等重试不双记）。
package dciexec

import (
	"context"
	"testing"
)

func TestEarnIO_SubscribeWrites(t *testing.T) {
	fakeIOCaptures = nil
	e := &Executor{ModeGate: liveModeGate, Store: &fakeStore{}}
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
	e := &Executor{ModeGate: liveModeGate, Store: &fakeStore{}}
	e.ioTransfer(context.Background(), "binance", "USDT", 100.0,
		"earn", "spot", "earn-red:p1:USDT", "")
	if len(fakeIOCaptures) != 1 {
		t.Fatalf("io rows=%d want 1", len(fakeIOCaptures))
	}
	if fakeIOCaptures[0].FromWallet != "earn" || fakeIOCaptures[0].ToWallet != "spot" {
		t.Fatalf("redeem direction wrong: %+v", fakeIOCaptures[0])
	}
}

// TestEarnIO_ConfigIDAligned 复审修件锚：唯一键含 config_id——exec 侧必须写
// 采集器同域账户 id（spot 行优先、earn 行兜底），否则 se- 同键也不撞。
func TestEarnIO_ConfigIDAligned(t *testing.T) {
	fakeIOCaptures = nil
	e := &Executor{ModeGate: liveModeGate, Store: &fakeStore{cfgID: 19}}
	e.ioTransfer(context.Background(), "binance", "USDT", 1.0,
		"spot", "earn", "se-99", "")
	if len(fakeIOCaptures) != 1 || fakeIOCaptures[0].ConfigID != 19 {
		t.Fatalf("config_id 应落采集器域: %+v", fakeIOCaptures)
	}
}

// TestEarnIOExtID 回执抽键——purchaseId/redeemId 串或数值 → se-<id>；无键/零值落兜底。
func TestEarnIOExtID(t *testing.T) {
	cases := []struct{ body, key, fallback, want string }{
		{`{"purchaseId":"12990451571"}`, "purchaseId", "fb", "se-12990451571"},
		{`{"redeemId":13002}`, "redeemId", "fb", "se-13002"},
		{`{"purchaseId":"0"}`, "purchaseId", "fb", "fb"},
		{`{"other":1}`, "purchaseId", "fb", "fb"},
		{`not-json`, "purchaseId", "fb", "fb"},
	}
	for _, c := range cases {
		if got := earnIOExtID(c.body, c.key, c.fallback); got != c.want {
			t.Fatalf("earnIOExtID(%q,%q)=%q want %q", c.body, c.key, got, c.want)
		}
	}
}
