// paper_first_gate_test.go —— D-593 复审⑧锚：判据函数≠孤儿——端到端拒穿断言。
package pgstore

import (
	"context"
	"testing"

	"nox/internal/platform/testutil"
)

func TestPaperFirstGate_EndToEnd(t *testing.T) {
	st, err := New(context.Background(), testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	noEvidence := func() (bool, error) { return false, nil }

	// ①负偏结构=结构性拒（dci_buy_low——naked_short_vol 禁真金径）
	ok, why, err := st.PaperFirstGate(ctx, "dci_buy_low", noEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if ok || why == "" {
		t.Fatalf("naked_short_vol 应拒, ok=%v why=%q", ok, why)
	}
	// ②豁免 D# 过（dci_sell_high_covered_call——D-593 承接位豁免）
	ok, _, err = st.PaperFirstGate(ctx, "dci_sell_high_covered_call", noEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("D-593 豁免行应过闸")
	}
	// ③无证无豁免=待纸面拒（funding_carry——directional_hedged 无豁免）
	ok, _, err = st.PaperFirstGate(ctx, "funding_carry", noEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("无证无豁免应待纸面拒")
	}
	// ④有证过（hasPaperEvidence=true）
	ok, _, err = st.PaperFirstGate(ctx, "funding_carry", func() (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("有证应过")
	}
	// ⑤未注册模板=fail-closed 拒
	ok, why, _ = st.PaperFirstGate(ctx, "bogus_template", noEvidence)
	if ok || why == "" {
		t.Fatal("未注册模板应拒")
	}
	// ⑥payoff_class NOT NULL：新插行缺 payoff_class 撞约束
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO fin_opp_template (id, mechanism, net_apr) VALUES ('probe_null','m','n')`); err == nil {
		t.Fatal("payoff_class NULL 应撞 NOT NULL 拒")
	}
}
