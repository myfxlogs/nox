// eval_test.go —— D-258 M4.1：评估卡实证（真测试库迁移面）。
package oppmatch

import (
	"context"
	"testing"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

func evalTestStore(t *testing.T) (*pgstore.Store, context.Context) {
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
	return st, ctx
}

// TestM41_DCICardViable：DCI 模板——费/滑点/市窗机理 n/a 证词+败单账本实证
// →全项有证据→net=毛报价>0 viable；一提案一卡 upsert 幂等+读回。
func TestM41_DCICardViable(t *testing.T) {
	st, ctx := evalTestStore(t)
	pid, _, err := st.CreateProposal(ctx, &pgstore.Proposal{
		TemplateID: "dci_buy_low", ExecTrack: "auto", Venue: "binance",
		Params: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	gross := 9.5
	r := Result{TemplateID: "dci_buy_low", APR: &gross}
	card := evalProposal(ctx, st, r, pid)
	if card.Verdict != "viable" || card.NetAPR == nil || *card.NetAPR != 9.5 {
		t.Fatalf("card=%+v", card)
	}
	if card.FeeAPR == nil || card.SlippageAPR == nil || card.WindowAPR == nil || card.FailureAPR == nil {
		t.Fatalf("机理 n/a 项应落 0 非 NULL: %+v", card)
	}
	if err := st.UpsertOppEval(ctx, card); err != nil {
		t.Fatal(err)
	}
	card.Verdict = "reject" // upsert 覆盖语义
	if err := st.UpsertOppEval(ctx, card); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetOppEval(ctx, pid)
	if err != nil || got == nil {
		t.Fatalf("read back: %v %+v", err, got)
	}
	if got.Verdict != "reject" || got.TemplateID != "dci_buy_low" {
		t.Fatalf("upsert 覆盖失: %+v", got)
	}
}

// TestM41_FundingCarry788Unevaluable：票面钦定锚——788% 毛报价有 facts 证据，
// 但费（双腿 taker×持有窗 窗无源）/失败成本（腿执行账本缺）无证据→
// unevaluable 零外推；有证项落值、无证项 NULL、missing 列名如实。
func TestM41_FundingCarry788Unevaluable(t *testing.T) {
	st, ctx := evalTestStore(t)
	// 撒 facts funding 种子让市窗项有证（中位 10）。
	for _, v := range []float64{8, 10, 12} {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO facts (kind, venue, symbol, value, unit, ts)
			 VALUES ('funding','tbinance','BTCUSDT',$1,'pct_annualized',now()-$2::interval)`,
			v, "96 hours"); err != nil {
			t.Fatal(err)
		}
	}
	pid, _, err := st.CreateProposal(ctx, &pgstore.Proposal{
		TemplateID: "funding_carry", ExecTrack: "auto", Venue: "binance",
		Params: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	gross := 788.619
	r := Result{TemplateID: "funding_carry", APR: &gross}
	card := evalProposal(ctx, st, r, pid)
	if card.Verdict != "unevaluable" {
		t.Fatalf("788 型应 unevaluable: %+v", card)
	}
	if card.FeeAPR != nil || card.FailureAPR != nil {
		t.Fatalf("无证项应 NULL: %+v", card)
	}
	if card.GrossAPR == nil || *card.GrossAPR != 788.619 {
		t.Fatalf("毛报价应落值: %+v", card)
	}
	miss, _ := card.EvidenceRefs["missing"].([]string)
	if len(miss) == 0 {
		t.Fatalf("missing 列表缺: %+v", card.EvidenceRefs)
	}
	if err := st.UpsertOppEval(ctx, card); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetOppEval(ctx, pid)
	if got == nil || got.Verdict != "unevaluable" || got.NetAPR != nil {
		t.Fatalf("unevaluable 卡净 APR 应 NULL: %+v", got)
	}
}
