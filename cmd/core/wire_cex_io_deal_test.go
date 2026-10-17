// wire_cex_io_deal_test.go —— D-607 Deal 事件落账锚（退回㉘锚伤：真五态断言）。
package main

import (
	"testing"
	"time"

	"nox/internal/data/adapter"
)

func TestMtDealIOSink(t *testing.T) {
	ts := time.Now()
	// ① Balance 落行——形态含 external_id/dedupKey/txid/方向/金额
	row, dk, ok := dealRow("110660304", 7, "USD", adapter.DealEvent{
		Ticket: 999, OrderTicket: 888, Type: 2, Profit: 1000, Ts: ts})
	if !ok || row.ExternalID != "mt5:deal:999" || dk != "mt5:ticket:888" ||
		row.TxID != "order:888" || row.Direction != "deposit" || row.Amount != 1000 || row.Ccy != "USD" {
		t.Fatalf("Balance 行形态错：%+v dk=%s ok=%v", row, dk, ok)
	}
	// ② Buy/Sell 交易族不落
	for _, typ := range []int32{0, 1, 13, 14} {
		if _, _, ok := dealRow("v", 1, "USD", adapter.DealEvent{Ticket: 1, Type: typ, Profit: 100}); ok {
			t.Fatalf("type %d 交易族应不落", typ)
		}
	}
	// ③ Credit/Correction 非交易族落（charge/correction/bonus/interest 同族）
	for _, typ := range []int32{3, 5} {
		if _, _, ok := dealRow("v", 1, "USD", adapter.DealEvent{Ticket: 1, Type: typ, Profit: 50}); !ok {
			t.Fatalf("type %d 非交易族应落", typ)
		}
	}
	// ④ 零额不落
	if _, _, ok := dealRow("v", 1, "USD", adapter.DealEvent{Ticket: 1, Type: 2, Profit: 0}); ok {
		t.Fatal("零额应不落")
	}
	// ⑤ 负额=withdraw 方向反
	row, _, ok = dealRow("v", 1, "USD", adapter.DealEvent{Ticket: 5, OrderTicket: 6, Type: 2, Profit: -50})
	if !ok || row.Direction != "withdraw" || row.Amount != 50 {
		t.Fatalf("withdraw 方向错：%+v", row)
	}
}
