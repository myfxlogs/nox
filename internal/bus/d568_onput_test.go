package bus

import (
	"testing"
	"time"
)

// TestD568_LedgerOnPut T1：Put → onPut 恰一次携 venueKey；MarkDirty 不触；nil 安全。
func TestD568_LedgerOnPut(t *testing.T) {
	l := NewBalanceLedger()
	var got []string
	l.SetOnPut(func(vk string) { got = append(got, vk) })

	l.Put("binance:perp", 100, 100, "USDT", "ws", time.Now())
	if len(got) != 1 || got[0] != "binance:perp" {
		t.Fatalf("Put want onPut once with binance:perp, got %v", got)
	}

	l.MarkDirty("binance:perp") // 标脏无新数据 → 不触发
	if len(got) != 1 {
		t.Fatalf("MarkDirty want no onPut, got %v", got)
	}

	l.Put("okx:unified", 50, 50, "USDT", "rest", time.Now())
	if len(got) != 2 || got[1] != "okx:unified" {
		t.Fatalf("second Put want onPut okx:unified, got %v", got)
	}
}

// TestD568_LedgerOnPutNilSafe：nil ledger / 未设 hook 均 no-op 不 panic。
func TestD568_LedgerOnPutNilSafe(t *testing.T) {
	var l *BalanceLedger
	l.SetOnPut(func(string) { t.Error("nil ledger onPut 不应触") })
	l.Put("binance:perp", 1, 1, "USDT", "ws", time.Now())

	l2 := NewBalanceLedger() // 未 SetOnPut
	l2.Put("gate:perp", 1, 1, "USD", "ws", time.Now())
}
