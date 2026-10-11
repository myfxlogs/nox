package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestD191_DCIOrderStore：幂等登记（request_id 防重）+收口+报价选档。
func TestD191_DCIOrderStore(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	s.pool.Exec(ctx, `DELETE FROM fin_dci_order WHERE request_id LIKE 'd191test%'`)
	strike, tenor := 100000.0, 7
	deliv := time.Now().AddDate(0, 0, 7)
	o := &DCIOrder{RequestID: "d191test|a", Venue: "binance", ProductRef: "P1",
		Underlying: "BTC", OptType: "CALL", Strike: &strike, InvestCcy: "BTC", Qty: 0.018, TenorDays: &tenor}
	id, created, err := s.CreateDCIOrder(ctx, o)
	if err != nil || !created {
		t.Fatalf("首登应 created: %v %v", id, err)
	}
	if _, again, _ := s.CreateDCIOrder(ctx, o); again {
		t.Fatal("request_id 重登应防重")
	}
	if err := s.FinishDCIOrder(ctx, id, "filled", `{"orderId":1}`); err != nil {
		t.Fatal(err)
	}
	// 报价选档：插两档，apy 优者中、tenor 超限者出。
	s.pool.Exec(ctx, `DELETE FROM fin_cex_dci_quote WHERE product_ref LIKE 'd191%'`)
	for _, r := range []CexDCIQuote{
		{Venue: "binance", Underlying: "BTC", InvestCcy: "BTC", OptType: "CALL",
			Strike: &strike, TenorDays: &tenor, APY: 0.05, ProductRef: "d191low", DeliveryTs: &deliv},
		{Venue: "binance", Underlying: "BTC", InvestCcy: "BTC", OptType: "CALL",
			Strike: &strike, TenorDays: &tenor, APY: 0.09, ProductRef: "d191hi", DeliveryTs: &deliv},
		{Venue: "binance", Underlying: "BTC", InvestCcy: "BTC", OptType: "CALL",
			Strike: &strike, APY: 0.99, ProductRef: "d191long", DeliveryTs: &deliv, TenorDays: intp(20)},
		{Venue: "binance", Underlying: "BTC", InvestCcy: "BTC", OptType: "P",
			Strike: &strike, TenorDays: &tenor, APY: 0.99, ProductRef: "d191put", DeliveryTs: &deliv},
	} {
		r2 := r
		if err := s.InsertCexDCIQuote(ctx, &r2); err != nil {
			t.Fatal(err)
		}
	}
	qs, err := s.ListDCIQuotes(ctx, "binance", "BTC", "CALL", "", 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 || qs[0].ProductRef != "d191hi" {
		t.Fatalf("应回 tenor≤14 CALL 档 apy 降序: %+v", qs)
	}
	qs2, err := s.ListDCIQuotes(ctx, "binance", "BTC", "PUT", "USDT", 90)
	if err != nil || len(qs2) != 0 {
		t.Fatalf("PUT/USDT 无档应空回: %v %+v", err, qs2)
	}
	// outstanding：filled 未到期计入、failed/已到期不计（R4 回填口径）。
	for _, oo := range []DCIOrder{
		{RequestID: "d191test|out1", Venue: "binance", ProductRef: "P2",
			Underlying: "BTC", OptType: "CALL", InvestCcy: "BTC", Qty: 0.02, TenorDays: &tenor},
		{RequestID: "d191test|out2", Venue: "binance", ProductRef: "P3",
			Underlying: "BTC", OptType: "PUT", InvestCcy: "USDT", Qty: 500, TenorDays: &tenor},
	} {
		o2 := oo
		oid, _, err := s.CreateDCIOrder(ctx, &o2)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.FinishDCIOrder(ctx, oid, "filled", "{}"); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.OutstandingDCIQty(ctx, "binance", "BTC")
	if err != nil || out < 0.038 { // d191test|a(0.018 filled)+out1(0.02)=0.038；out2 USDT 不计
		t.Fatalf("outstanding BTC 回填错: %v %.4f", err, out)
	}
	s.pool.Exec(ctx, `DELETE FROM fin_dci_order WHERE request_id LIKE 'd191test%'`)

	// 回执入账。
	qty := 0.018
	pid, err := s.InsertPersonalPosition(ctx, "BTC", "other", "BTCUSDT", "d191 回执", &qty, nil)
	if err != nil || pid == 0 {
		t.Fatalf("回执入账: %v", err)
	}
	s.pool.Exec(ctx, `DELETE FROM personal_positions WHERE id=$1`, pid)
}

func intp(i int) *int { return &i }
