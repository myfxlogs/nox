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
	qs, err := s.ListDCIQuotes(ctx, "binance", "BTC", "CALL", "", 14, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 || qs[0].ProductRef != "d191hi" {
		t.Fatalf("应回 tenor≤14 CALL 档 apy 降序: %+v", qs)
	}
	// 共享测试库残行（r8/r13 跨轮残留）先清再对空回锚。
	s.pool.Exec(ctx, `DELETE FROM fin_cex_dci_quote WHERE opt_type='PUT'`)
	qs2, err := s.ListDCIQuotes(ctx, "binance", "BTC", "PUT", "USDT", 90, 0)
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

// TestD191R8_StrikeBoundAndFresh：strike 界+6h 时效窗下推 SQL——
// 高 apy 近档被界滤、深档选得中；ts>6h 旧帧不参与。
func TestD191R8_StrikeBoundAndFresh(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	s5, s7 := 95000.0, 70000.0
	td := 7
	rows := []CexDCIQuote{
		{Venue: "binance", Underlying: "BTC", InvestCcy: "USDT", OptType: "PUT",
			Strike: &s5, TenorDays: &td, APY: 0.50, ProductRef: "r8near"}, // 高apy但 95000>bound 界滤
		{Venue: "binance", Underlying: "BTC", InvestCcy: "USDT", OptType: "PUT",
			Strike: &s7, TenorDays: &td, APY: 0.08, ProductRef: "r8deep"}, // 深档达标选得中
	}
	for i := range rows {
		if err := s.InsertCexDCIQuote(ctx, &rows[i]); err != nil {
			t.Fatal(err)
		}
	}
	// 旧帧（ts>6h）不参与：直插 ts 倒拨 7h。
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_cex_dci_quote (venue, underlying, invest_ccy, strike, opt_type,
			tenor_days, apy, product_ref, ts)
		VALUES ('binance','BTC','USDT',60000,'PUT',7,0.99,'r8stale', now()-interval '7 hours')`); err != nil {
		t.Fatal(err)
	}
	qs, err := s.ListDCIQuotes(ctx, "binance", "BTC", "PUT", "USDT", 90, 80000)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range qs {
		if q.ProductRef == "r8near" || q.ProductRef == "r8stale" {
			t.Fatalf("界滤/旧帧漏网: %+v", q)
		}
	}
	if len(qs) == 0 || qs[0].ProductRef != "r8deep" {
		t.Fatalf("深档应选得中: %+v", qs)
	}
}

// TestD191R13_DedupLatest：同 product_ref 多版本入窗只最新帧进候选——
// 旧版 NULL 行（order_ref 缺）不挤档，老版本高 apy 不抢选。
func TestD191R13_DedupLatest(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `
		DELETE FROM fin_cex_dci_quote WHERE product_ref LIKE 'r13%'`); err != nil {
		t.Fatal(err)
	}
	// r13dup 旧版（order_ref/min 缺、apy 高）+新版（字段齐、apy 低）同窗。
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_cex_dci_quote (venue, underlying, invest_ccy, strike, opt_type,
			tenor_days, apy, product_ref, order_ref, min_amount, ts) VALUES
		('binance','BTC','USDT',70000,'PUT',7,0.99,'r13dup','',NULL, now()-interval '5 minutes'),
		('binance','BTC','USDT',70000,'PUT',7,0.05,'r13dup','ORD13',10, now())`); err != nil {
		t.Fatal(err)
	}
	qs, err := s.ListDCIQuotes(ctx, "binance", "BTC", "PUT", "USDT", 90, 80000)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, q := range qs {
		if q.ProductRef == "r13dup" {
			n++
			if q.OrderRef != "ORD13" || q.APY != 0.05 {
				t.Fatalf("应取最新帧: %+v", q)
			}
		}
	}
	if n != 1 {
		t.Fatalf("同品应只一帧进候选, got %d", n)
	}
}
