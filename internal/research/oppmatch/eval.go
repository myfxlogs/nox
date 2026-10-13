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
		// 申赎失败通道无机读账本——failure 缺证据。
	case r.TemplateID == "funding_carry":
		// funding=永续随市：费=双腿 taker×365/持有窗——持有窗无账本证据源→缺；
		// 滑点=cex_spread；市窗=facts funding 30d 中位衰减；失败=腿执行账本缺。
		if b, ok, err := st.LatestSpreadBps(ctx, "BTCUSDT"); err == nil && ok {
			in.Slippage = econ.Term{Value: b / 100, Evidence: "fin_cex_spread:BTCUSDT"}
		}
		if m, ok, err := st.MedianFactValue(ctx, "funding", evalWindow); err == nil && ok && r.APR != nil {
			w := *r.APR - m
			if w < 0 {
				w = 0
			}
			in.Window = econ.Term{Value: w, Evidence: "facts:funding 30d中位"}
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
