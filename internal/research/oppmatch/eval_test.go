// eval_test.go —— D-258 M4.1：评估卡实证（真测试库迁移面）。
package oppmatch

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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
// 费/失败证源补强后接 taker 中位×持有窗+腿败账本（D-2xx）——本锚自隔离种子
// 两源，余缺=滑点（fin_cex_spread 无行）→ 仍 unevaluable 零外推，missing 如实列。
func TestM41_FundingCarry788Unevaluable(t *testing.T) {
	st, ctx := evalTestStore(t)
	// 自隔离：持仓面清零+perp taker 统一 5bps——fee/failure 证据确定性。
	if _, err := st.Pool().Exec(ctx, `TRUNCATE positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE exchange_configs SET taker_fee_bps=5.0 WHERE venue_type='perp'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at)
		VALUES ('m41','PERP_FUNDING','CLOSED', now()-interval '25 hours', now()-interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
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
	if card.FeeAPR == nil || card.FailureAPR == nil {
		t.Fatalf("补强后 fee/failure 应有证据落值: %+v", card)
	}
	if missing, _ := card.EvidenceRefs["missing"].([]string); len(missing) != 1 || missing[0] != "slippage" {
		t.Fatalf("missing 应仅余 slippage: %+v", card.EvidenceRefs)
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

// TestEvalCard_EarnFailureSrc D-2xx①：earn 模板 failure 证源=fin_venue_reject
// earn_redeem 撞坑累计——种子一坑（hits=2）后卡面 failure 项出证据出 verdict。
func TestEvalCard_EarnFailureSrc(t *testing.T) {
	st, ctx := evalTestStore(t)
	pid, _, err := st.CreateProposal(ctx, &pgstore.Proposal{
		TemplateID: "cash_mgmt", ExecTrack: "auto", Venue: "binance",
		Params: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `DELETE FROM fin_venue_reject WHERE op='earn_redeem'`); err != nil {
		t.Fatal(err)
	}
	if err := st.LearnVenueReject(ctx, "binance", "earn_redeem", "code:-1102|amount", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := st.LearnVenueReject(ctx, "binance", "earn_redeem", "code:-1102|amount", time.Hour); err != nil {
		t.Fatal(err)
	}
	gross := 3.0
	card := evalProposal(ctx, st, Result{TemplateID: "cash_mgmt", APR: &gross}, pid)
	ev := card.EvidenceRefs["terms"].(map[string]string)
	if ev["failure"] == "" || !strings.Contains(ev["failure"], "earn_redeem 2败") {
		t.Fatalf("failure 证据缺: %+v", ev)
	}
	if card.Verdict == "unevaluable" && strings.Contains(fmt.Sprint(card.EvidenceRefs["missing"]), "failure") {
		t.Fatalf("failure 仍缺: %+v", card.EvidenceRefs)
	}
}

// TestEvalCard_FundingFeeSrc D-2xx②：funding_carry 费源=taker 中位×2 腿按
// PERP_FUNDING realized 持有窗年化；失败=腿败账本（0 败=零损耗证据）。
func TestEvalCard_FundingFeeSrc(t *testing.T) {
	st, ctx := evalTestStore(t)
	pid, _, err := st.CreateProposal(ctx, &pgstore.Proposal{
		TemplateID: "funding_carry", ExecTrack: "proposal", Venue: "okx",
		Params: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `TRUNCATE positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `UPDATE exchange_configs SET taker_fee_bps=5.0 WHERE venue_type='perp'`); err != nil {
		t.Fatalf("seed taker: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at)
		VALUES ('t1','PERP_FUNDING','CLOSED', now()-interval '25 hours', now()-interval '1 hour'),
		       ('t2','PERP_FUNDING','CLOSED', now()-interval '49 hours', now()-interval '1 hour')`); err != nil {
		t.Fatalf("seed positions: %v", err)
	}
	gross := 12.0
	card := evalProposal(ctx, st, Result{TemplateID: "funding_carry", APR: &gross}, pid)
	ev := card.EvidenceRefs["terms"].(map[string]string)
	if ev["fee"] == "" || !strings.Contains(ev["fee"], "taker中位5.00bps") {
		t.Fatalf("fee 证据缺: %+v", ev)
	}
	if ev["failure"] != "position_legs 0腿败/30d" {
		t.Fatalf("failure 证据: %q", ev["failure"])
	}
	if card.FeeAPR == nil || *card.FeeAPR <= 0 {
		t.Fatalf("fee_apr 未落: %+v", card)
	}
}
