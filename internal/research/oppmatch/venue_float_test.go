package oppmatch

import (
	"testing"
)

// TestVenueFloat_RailSet earn rail 多 venue 化锚：模板凭据域 ⊆ rail 覆盖集→
// 零告警（对齐态）；集外凭据→warn 一次（漂移态），复扫 24h 去重不重发。
func TestVenueFloat_RailSet(t *testing.T) {
	st, ctx := d514Store(t)
	if _, err := st.Pool().Exec(ctx,
		`DELETE FROM alerts WHERE message LIKE '模板venue悬空%'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM alerts WHERE message LIKE '模板venue悬空%'`)
	})
	var n int
	count := func() {
		if err := st.Pool().QueryRow(ctx,
			`SELECT count(*) FROM alerts WHERE message LIKE '模板venue悬空%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
	}
	// 对齐态：earn_apr_lending 凭据 okx:spot+binance:spot 皆在 EarnExecVenues 集内。
	checkVenueFloat(ctx, st, []Template{{
		ID: "earn_apr_lending",
		Conds: []Cond{
			{Kind: "credential", Subject: "okx:spot"},
			{Kind: "credential", Subject: "binance:spot"},
		},
	}})
	count()
	if n != 0 {
		t.Fatalf("凭据域⊆覆盖集不应告警: %d", n)
	}
	// 漂移态：dci_buy_low 凭据 gate:dual 不在 DCIExecVenues（binance/okx）集内→warn。
	checkVenueFloat(ctx, st, []Template{{
		ID: "dci_buy_low",
		Conds: []Cond{
			{Kind: "credential", Subject: "gate:dual"},
			{Kind: "capital", Subject: "USDT"}, // 非 credential 不入判定
		},
	}})
	count()
	if n != 1 {
		t.Fatalf("集外凭据应 warn 一次: %d", n)
	}
	// episode 防抖：复扫同签名 24h 去重零重发。
	checkVenueFloat(ctx, st, []Template{{
		ID:    "dci_buy_low",
		Conds: []Cond{{Kind: "credential", Subject: "gate:dual"}},
	}})
	count()
	if n != 1 {
		t.Fatalf("复扫去重失效: %d", n)
	}
}

// TestEarnQuoteVenues 提案 venue=报价所取 venue：rail 集内有 USDT 报价行的
// venue 各出（排序定序），零行 venue 不产。
func TestEarnQuoteVenues(t *testing.T) {
	st, ctx := d514Store(t)
	if _, err := st.Pool().Exec(ctx, `DELETE FROM fin_cex_earn_apr
		WHERE venue IN ('okx','gate','binance')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO fin_cex_earn_apr
		(venue, ccy, product_type, apr, ts) VALUES
		('okx','USDT','flex',0.0365,now()),('gate','USDT','flex',0.021,now()),
		('okx','BTC','flex',0.011,now())`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM fin_cex_earn_apr
			WHERE venue IN ('okx','gate','binance')`)
	})
	venues, aprs := earnQuoteVenues(ctx, st)
	if len(venues) != 2 || venues[0] != "gate" || venues[1] != "okx" {
		t.Fatalf("报价 venue 枚举错: %v", venues)
	}
	if aprs["okx"] != 0.0365 || aprs["gate"] != 0.021 {
		t.Fatalf("venue apr 错价: %v", aprs)
	}
}
