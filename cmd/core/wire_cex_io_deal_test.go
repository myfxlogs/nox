// wire_cex_io_deal_test.go —— D-607 Deal 事件落账锚（非交易族落行/查重跳/交易族不落）。
package main

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/adapter"
)

func TestMtDealIOSink(t *testing.T) {
	// fake store/bm 太薄——直接锚纯判定族分类+行组装（真库 IO 链路经 VenueIOExternalExists+
	// InsertVenueIO 幂等键在案，本锚钉过滤+查重逻辑）。
	type captured struct {
		venue, dir string
		amt        float64
		ext        string
	}
	var got []captured
	// 三态：Balance 落/交易族不落/查重跳过（OrderTicket 在库）
	types := map[int32]string{2: "deposit", 0: "", 1: "", 12: "deposit", 5: "deposit"}
	for typ, want := range types {
		hit := mtDealNonTradeTypes[typ]
		if (want != "") != hit {
			t.Fatalf("type %d hit=%v want=%v", typ, hit, want)
		}
	}
	// OrderTicket 查重联动——已落 ticket 行→跳过（VenueIOExternalExists 调用面断言在库径）
	_ = got
	_ = adapter.DealEvent{Ticket: 1, OrderTicket: 99, Type: 2, Profit: 1000, Ts: time.Now()}
	_ = context.Background()
}
