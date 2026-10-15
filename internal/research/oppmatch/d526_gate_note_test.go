package oppmatch

// d526_gate_note_test.go —— D-526 返修① 锚：过闸 σ/界/尾损注记落提案 params 可检索。

import (
	"context"
	"strings"
	"testing"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

func TestD526_GateNoteInProposal(t *testing.T) {
	// σ 注口小值（同 d514 径——宽界保 fixture 达标、tail≈0 不挡溢价）。
	sigmaFn = func(context.Context, *pgstore.Store) (float64, string, error) {
		return 0.001, "test-stub", nil
	}
	t.Cleanup(func() { sigmaFn = nil })
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// 前置清理+条件种子（dci_buy_low 全条件可达：凭据改指 binance:spot——
	// 'gate:dual' venue_type 非枚举键永不可达，测试 DB 内改 cond 属 fixture 域）。
	if _, err := st.Pool().Exec(ctx, `
		DELETE FROM fin_opp_eval WHERE template_id='dci_buy_low';
		DELETE FROM fin_proposal WHERE template_id='dci_buy_low';
		DELETE FROM fin_dci_order WHERE product_ref LIKE 'D526%';
		DELETE FROM fin_cex_dci_quote WHERE product_ref LIKE 'D526%';
		DELETE FROM fin_cex_spread WHERE symbol='BTC' AND venue_buy='d526b';
		DELETE FROM fin_cex_earn_apr WHERE venue='d526e' OR ccy='USDT';
		DELETE FROM fin_capability_constraint WHERE subject LIKE 'dormant:dci_buy_low%';
		UPDATE fin_opp_template_cond SET subject='binance:spot'
		  WHERE template_id='dci_buy_low' AND kind='credential';
		INSERT INTO exchange_configs (venue, venue_type, api_key, api_secret)
		  VALUES ('binance','spot','d526k','d526s')
		  ON CONFLICT DO NOTHING;
		INSERT INTO fin_capability_permission (venue, permission, state)
		  VALUES ('binance','dual_order','present')
		  ON CONFLICT (venue, permission) DO UPDATE SET state='present';
		INSERT INTO fin_venue_account_snapshot
		  (venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency)
		  VALUES ('d526a','spot',5000,5000,5000,'USDT');
		UPDATE fin_capability_constraint SET value_num=1e9
		  WHERE subject='dci_open_notional_cap_usdt';
		INSERT INTO fin_cex_spread (symbol, venue_buy, venue_sell, price_buy, price_sell, spread_bps)
		  VALUES ('BTC','d526b','d526s',80000,80000,0);
		INSERT INTO fin_cex_dci_quote
		  (venue, underlying, invest_ccy, strike, opt_type, delivery_ts, tenor_days, apy, product_ref)
		  VALUES ('binance','BTC','USDT',72000,'PUT',now()+interval '16 days',16,10,'D526-PUT');
		INSERT INTO fin_cex_earn_apr (venue, ccy, product_type, apr)
		  VALUES ('d526e','USDT','flexible',0.02)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { // 共享测试库：cond/约束 fixture 归位，不污染邻测
		_, _ = st.Pool().Exec(context.Background(), `
			UPDATE fin_opp_template_cond SET subject='gate:dual'
			  WHERE template_id='dci_buy_low' AND kind='credential';
			UPDATE fin_capability_constraint SET value_num=1500
			  WHERE subject='dci_open_notional_cap_usdt';
			DELETE FROM exchange_configs WHERE api_key='d526k';
			DELETE FROM fin_proposal WHERE template_id='dci_buy_low' AND venue='binance';
			DELETE FROM fin_venue_account_snapshot WHERE venue='d526a';
			DELETE FROM fin_cex_earn_apr WHERE venue='d526e';
			DELETE FROM fin_cex_dci_quote WHERE product_ref LIKE 'D526%';
			DELETE FROM fin_cex_spread WHERE venue_buy='d526b';
			DELETE FROM fin_capability_permission WHERE venue='binance' AND permission='dual_order'`)
	})
	if _, _, err := RunOnce(ctx, st, 10000, 1000); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	var note string
	if err := st.Pool().QueryRow(ctx, `
		SELECT COALESCE(params->>'gate_note','') FROM fin_proposal
		WHERE template_id='dci_buy_low' AND venue='binance'
		ORDER BY id DESC LIMIT 1`).Scan(&note); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"σ=", "界=", "tail=", "net="} {
		if !strings.Contains(note, k) {
			t.Fatalf("gate_note 缺 %s: %q", k, note)
		}
	}
}
