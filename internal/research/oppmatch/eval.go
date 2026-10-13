// eval.go —— D-258 M4.1：提案评估卡装配面。econ.Evaluate 纯算，
// 本件以 venue/账本查询喂 Term；机理不适用项给 mechanism: 证据，
// 无查询源=Evidence 空→卡面如实列 missing→unevaluable（零外推）。
package oppmatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"nox/internal/research/econ"
	"nox/internal/store/pgstore"
)

// evalWindow 证据窗口（账本/报价中位口径 30d，对齐报价面 retention）。
const evalWindow = 30 * 24 * time.Hour

// evalProposal 提案→评估卡（五项逐项证源；读败项如实记 missing 不外推）。
// 返回卡面供 UpsertOppEval 落行。
func evalProposal(ctx context.Context, st *pgstore.Store, r Result, pid int64) *pgstore.OppEval {
	in := econ.EvalInput{TemplateID: r.TemplateID}
	if r.APR != nil {
		in.Gross = econ.Term{Value: *r.APR, Evidence: "quoteFor:" + r.TemplateID}
	}
	dci := strings.HasPrefix(r.TemplateID, "dci_") || r.TemplateID == "option_spread"
	earn := r.TemplateID == "earn_apr_lending" || r.TemplateID == "cash_mgmt"
	switch {
	case dci:
		// DCI 申购：venue 报价 APY 申购即锁——费/滑点/市窗机理不适用；
		// 失败成本=败单账本实证（拒绝零资金损耗）。
		in.Fee = econ.Term{Evidence: "mechanism:DCI申购费不适用"}
		in.Slippage = econ.Term{Evidence: "mechanism:申购锁价滑点不适用"}
		in.Window = econ.Term{Evidence: "mechanism:APY申购即锁市窗不适用"}
		if n, err := st.DCIRejectCount(ctx, r.TemplateID, evalWindow); err == nil {
			in.Failure = econ.Term{Evidence: fmt.Sprintf("fin_dci_order %d败/30d零资金损耗", n)}
		}
	case earn:
		in.Fee = econ.Term{Evidence: "mechanism:理财申赎费不适用"}
		in.Slippage = econ.Term{Evidence: "mechanism:理财报价滑点不适用"}
		if m, ok, err := st.MedianEarnAPR(ctx, evalWindow); err == nil && ok {
			w := *r.APR - m
			if w < 0 {
				w = 0
			}
			in.Window = econ.Term{Value: w, Evidence: "fin_cex_earn_apr 30d中位"}
		}
		// D-2xx 失败证源=fin_venue_reject earn_redeem 撞坑累计（口径同 DCI：
		// 申赎拒绝发生在资金移动前=零资金损耗）。
		if n, err := st.RejectHits(ctx, "earn_redeem", evalWindow); err == nil {
			in.Failure = econ.Term{Evidence: fmt.Sprintf("fin_venue_reject earn_redeem %d败/30d零资金损耗", n)}
		}
	case r.TemplateID == "funding_carry":
		// funding=永续随市：滑点=cex_spread；市窗=facts funding 30d 中位衰减。
		// D-2xx 费=perp taker 中位×双腿 按 realized 持有窗年化（PERP_FUNDING
		// CLOSED 时长账本；零行=缺证据不外推）；失败=position_legs FAILED 账本
		// ——0 败=零损耗证据，>0=成本未计价 fail-closed 仍缺（不估单价）。
		// D-277 G4 修复：fin_cex_spread.symbol 写侧存基础币形（BTC/SOL/TRX/ETH），
		// 键须用基名——"BTCUSDT" 永不命中（8526 行/日鲜写侧键义不动）。
		if b, ok, err := st.LatestSpreadBps(ctx, "BTC"); err == nil && ok {
			in.Slippage = econ.Term{Value: b / 100, Evidence: "fin_cex_spread:BTC"}
		}
		if m, ok, err := st.MedianFactValue(ctx, "funding", evalWindow); err == nil && ok && r.APR != nil {
			w := *r.APR - m
			if w < 0 {
				w = 0
			}
			in.Window = econ.Term{Value: w, Evidence: "facts:funding 30d中位"}
		}
		if t, ok, err := st.MedianTakerFeeBps(ctx); err == nil && ok {
			if hh, ok2, err2 := st.MedianHoldHours(ctx, []string{"PERP_FUNDING"}, evalWindow); err2 == nil && ok2 && hh > 0 {
				// fee_apr = 双腿 taker%×（8760h/中位持有h）年化
				in.Fee = econ.Term{Value: t * 2 / 100 * (8760 / hh),
					Evidence: fmt.Sprintf("exchange_configs taker中位%.2fbps×2腿 / PERP_FUNDING 中位持有%.1fh", t, hh)}
			}
		}
		if n, err := st.LegFailCount(ctx, []string{"PERP_FUNDING"}, evalWindow); err == nil && n == 0 {
			in.Failure = econ.Term{Evidence: "position_legs 0腿败/30d"}
		}
	}
	card := econ.Evaluate(in)
	ev := map[string]any{"terms": card.Evidence}
	if len(card.Missing) > 0 {
		ev["missing"] = card.Missing
	}
	fp := func(i int) *float64 {
		if _, ok := card.Evidence[terms[i]]; !ok {
			return nil // 缺证据项=NULL 不落 0 伪值
		}
		v := card.Terms[i]
		return &v
	}
	var net *float64
	if card.Verdict != "unevaluable" {
		net = &card.Net
	}
	return &pgstore.OppEval{
		ProposalID: pid, TemplateID: r.TemplateID,
		GrossAPR: fp(0), FeeAPR: fp(1), SlippageAPR: fp(2),
		FailureAPR: fp(3), WindowAPR: fp(4), NetAPR: net,
		Verdict: card.Verdict, EvidenceRefs: ev,
	}
}

var terms = [5]string{"gross", "fee", "slippage", "failure", "window"}
