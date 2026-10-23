// okx_ws_books5_test.go —— 返修④锚测：价语义+原生形+节流+安全解析。
package exchange

import (
	"context"
	"testing"
	"time"
)

func TestHandleBooks5_PriceSemantics(t *testing.T) {
	var got []CexDepthRowLite
	o := &OKXWebSocket{
		depthQ:    make(chan CexDepthRowLite, 4),
		depthLast: map[string]time.Time{},
	}
	o.SetDepthEmit(func(_ context.Context, rows []CexDepthRowLite) error {
		got = append(got, rows...)
		return nil
	})
	var msg wsMsg
	msg.Arg.Channel = "books5"
	msg.Arg.InstID = "BTC-USDT-SWAP"
	msg.Data = []map[string]any{{
		"bids": []any{[]any{"67000.5", "3.0"}},
		"asks": []any{[]any{"67001.0", "2.0"}},
	}}
	o.handleBooks5(context.Background(), msg)
	row := <-o.depthQ
	if row.Bid1USD != 67000.5 || row.Ask1USD != 67001.0 {
		t.Fatalf("列语义=顶档价非名义量: %+v", row)
	}
	if row.Symbol != "BTC-USDT-SWAP" {
		t.Fatalf("Symbol=instID 原生形: %s", row.Symbol)
	}
	// 节流：同窗次帧丢弃。
	o.handleBooks5(context.Background(), msg)
	if len(o.depthQ) != 0 {
		t.Fatalf("节流窗内帧应弃: %d", len(o.depthQ))
	}
	// 安全解析：坏载荷不 panic 不 emit。
	msg.Data = []map[string]any{{"bids": []any{[]any{123, "x"}}, "asks": []any{}}}
	func() { defer func() { _ = recover() }(); o.handleBooks5(context.Background(), msg) }()
	if len(o.depthQ) != 0 {
		t.Fatalf("坏载荷不 emit")
	}
}
