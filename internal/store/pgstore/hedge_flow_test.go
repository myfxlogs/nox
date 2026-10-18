package pgstore

// hedge_flow_test.go —— ListHedgeFlowPage 锚：双源 UNION 归一+过滤+排序对拍。
import (
	"context"
	"testing"
	"time"
)

func TestListHedgeFlowPage_UnionNormalize(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	must := func(q string, args ...any) {
		if _, err := st.pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	base := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	// trx 两腿：rental_income 新 + deposit 旧；cheat 腿与 vote 腿应被滤
	must(`INSERT INTO fin_trx_txs (tx_id, ts, kind, amount_sun, counterparty, cheat_status, fetched_at)
		VALUES ('trx-deposit',$1,'deposit',5000000000,'addr-x',false,$1),
		       ('trx-rental',$2,'rental_income',120000000,'sr-a',false,$2),
		       ('trx-cheat',$3,'deposit',999000000,'addr-c',true,$3),
		       ('trx-vote',$3,'vote',0,NULL,false,$3)`, base.Add(-2*time.Hour), base, base.Add(-3*time.Hour))
	// cmc 两腿：PAYMENTS_IN→deposit + OVERNIGHT_SETTLEMENT→overnight；SELL_TRADE 滤
	must(`INSERT INTO fin_cmc_history (account_id, transaction_id, transaction_type, amount, currency, transaction_time)
		VALUES (1,'cmc-pay','PAYMENTS_IN',100.50,'USD',$1),
		       (1,'cmc-ov','OVERNIGHT_SETTLEMENT',-1.25,'USD',$2),
		       (1,'cmc-sell','SELL_TRADE',50.0,'USD',$3)`,
		base.Add(-time.Hour), base.Add(-30*time.Minute), base.Add(-4*time.Hour))

	all, err := st.ListHedgeFlowPage(ctx, "", 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// 断言①：作弊腿/vote 腿/SELL_TRADE 腿全滤——4 行通过
	if len(all) != 4 {
		t.Fatalf("want 4 rows (cheat/vote/SELL_TRADE filtered), got %d: %+v", len(all), all)
	}
	// 断言②：ts 降序——rental(base) → cmc-ov(-30m) → cmc-pay(-1h) → trx-deposit(-2h)
	if all[0].TxID != "trx-rental" || all[0].Source != "trx" || all[0].Kind != "rental_income" || all[0].AmountValue != 120 || all[0].AmountUnit != "TRX" {
		t.Fatalf("row0 mismatch: %+v", all[0])
	}
	if all[1].TxID != "cmc-ov" || all[1].Kind != "overnight" || all[1].AmountUnit != "USD" {
		t.Fatalf("row1 cmc 归一 mismatch: %+v", all[1])
	}
	// 断言③：source 过滤
	trxOnly, _ := st.ListHedgeFlowPage(ctx, "trx", 50, 0)
	if len(trxOnly) != 2 {
		t.Fatalf("trx filter want 2, got %d", len(trxOnly))
	}
	cmcOnly, _ := st.ListHedgeFlowPage(ctx, "cmc", 50, 0)
	if len(cmcOnly) != 2 {
		t.Fatalf("cmc filter want 2, got %d", len(cmcOnly))
	}
	// 断言④：分页 offset
	p1, _ := st.ListHedgeFlowPage(ctx, "", 1, 1)
	if len(p1) != 1 || p1[0].TxID != "cmc-ov" {
		t.Fatalf("offset page mismatch: %+v", p1)
	}
}
