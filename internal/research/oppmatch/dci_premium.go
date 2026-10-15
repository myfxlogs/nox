// dci_premium.go —— DCI 提案三闸：D-514 溢价闸（premium_ann<max(3×earn_apr,8%)
// →「无达标档」）+ D-526 尾险定价闸（σ 驱动 OTM 界+选档目标 premium−tail 最优
// +在仓名义帽）。premium_ann=fin_cex_dci_quote 档 apy（venue 原生年化口径）；
// σ_daily=venue 日 K 实算（dci_sigma.go）；尾损=BS 近似年化（gate.TailCostAnn）。
// 三闸皆 fail-closed：读败/σ 缺/约束缺/报价缺=不产提案不臆放。
// 只挡 DCI：earn/其他模板零动。
package oppmatch

import (
	"context"
	"fmt"

	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// dciPremiumFloor D-514 闸地板（业主令钉值：8%）。
const dciPremiumFloor = 0.08

// dciStrikeBound D-526 快照同界：现货参照×σ 驱动比率界（tenor=tenor_max 最严口径；
// 无参照/σ 缺/约束缺→0=fail-closed 无界快照降级）。
func dciStrikeBound(ctx context.Context, st *pgstore.Store, opt string, sigma float64) float64 {
	spot, err := st.LatestSpreadSpotRef(ctx, "BTC")
	if err != nil || spot <= 0 {
		return 0
	}
	cons, err := st.ListCapabilityConstraints(ctx)
	if err != nil {
		return 0
	}
	k, tmax, ok := gate.SigmaTailParams(cons, opt)
	if !ok || sigma <= 0 {
		return 0
	}
	return gate.StrikeBound(spot, gate.SigmaBoundRatio(opt, sigma, k, tmax))
}

// dciPremiumGate 溢价闸+尾险闸判定：返回过闸与否+拒因注记。
// 注记带 σ/界/尾损三要素可审计（票面⑤）。
func dciPremiumGate(ctx context.Context, st *pgstore.Store, venue string) (bool, string) {
	cons, cerr := st.ListCapabilityConstraints(ctx)
	if cerr != nil {
		return false, fmt.Sprintf("约束表读败：%v", cerr)
	}
	k, tmax, ok := gate.SigmaTailParams(cons, "PUT")
	if !ok {
		return false, "尾险约束缺（dci_put_otm_k/dci_tenor_max_days 行——fail-closed 不产提案）"
	}
	// 在仓名义帽：跨 venue 未到期 PUT 名义合计≥cap→不产提案（票面④）。
	var capRow *float64
	for _, c := range cons {
		if c.Subject == "dci_open_notional_cap_usdt" {
			capRow = c.ValueNum
		}
	}
	if capRow == nil {
		return false, "尾险约束缺（dci_open_notional_cap_usdt 行——fail-closed 不产提案）"
	}
	openN := 0.0
	for _, v := range dciProposalVenues {
		q, qerr := st.OutstandingDCIQty(ctx, v, "USDT")
		if qerr != nil {
			return false, fmt.Sprintf("在仓名义读败 %s：%v", v, qerr)
		}
		openN += q
	}
	if openN >= *capRow {
		return false, fmt.Sprintf("在仓名义帽：open=%.0f≥cap=%.0f USDT", openN, *capRow)
	}
	sigma, src, serr := fetchSigma(ctx, st)
	if serr != nil {
		return false, serr.Error()
	}
	spot, err := st.LatestSpreadSpotRef(ctx, "BTC")
	if err != nil || spot <= 0 {
		return false, "BTC 现货参照缺（界不可算不臆放）"
	}
	bound := gate.StrikeBound(spot, gate.SigmaBoundRatio("PUT", sigma, k, tmax))
	qs, err := st.ListDCIQuotes(ctx, venue, "BTC", "PUT", "USDT", int(tmax), bound)
	if err != nil {
		return false, fmt.Sprintf("%s PUT 报价读败：%v", venue, err)
	}
	if len(qs) == 0 {
		return false, fmt.Sprintf("%s PUT 达标档报价缺（σ 界 %.0f 内无档）", venue, bound)
	}
	// 选档目标=net(premium_ann−tail_cost_ann) 最优（票面③）；tenor 用档实值。
	var best *pgstore.CexDCIQuote
	var bestNet, bestPrem, bestTail float64
	for i := range qs {
		qq := &qs[i]
		if qq.Strike == nil || qq.TenorDays == nil {
			continue
		}
		prem := qq.APY
		if venue == "binance" {
			prem /= 100 // binance apy=%口径实拍归一小数（cexscan/dci.go 注）
		}
		tail := gate.TailCostAnn("PUT", spot, *qq.Strike, sigma, *qq.TenorDays)
		if net := prem - tail; best == nil || net > bestNet {
			best, bestNet, bestPrem, bestTail = qq, net, prem, tail
		}
	}
	if best == nil {
		return false, fmt.Sprintf("%s PUT 达标档 strike/tenor 缺（尾损不可算不臆放）", venue)
	}
	if bestNet <= 0 {
		return false, fmt.Sprintf(
			"尾损盖溢价：premium_ann %.2f%%−tail %.2f%%=net %.2f%%≤0（σ=%.2f%% 界=%.0f strike=%.0f tenor=%dd）",
			bestPrem*100, bestTail*100, bestNet*100, sigma*100, bound, *best.Strike, *best.TenorDays)
	}
	// 基准=同币种（投入币 USDT）跨 venue 理财顶档——异币种营销档不入基准
	// （BETH 365% 类 3× 地板虚高实证）；earn 面缺=退纯地板不臆放。
	benchVenue, earnApr, benchTS, ok, err := st.MaxEarnAPRByCcy(ctx, "USDT")
	if err != nil {
		return false, fmt.Sprintf("earn_apr USDT 基准读败：%v", err)
	}
	if !ok {
		earnApr = 0 // earn 面缺≠无限额——闸退纯地板 8%
	}
	thr := dciPremiumFloor
	if t := 3 * earnApr; t > thr {
		thr = t
	}
	if bestPrem < thr {
		return false, fmt.Sprintf("溢价低于闸：premium_ann %.2f%% < max(3×USDT_earn %s %.2f%%@%s, 8%%)=%.2f%%",
			bestPrem*100, benchVenue, earnApr*100, benchTS.Format("01-02 15:04"), thr*100)
	}
	return true, fmt.Sprintf("基准=USDT earn_apr %s %.2f%% @%s；σ=%.2f%%（%s）界=%.0f tail=%.2f%% net=%.2f%%",
		benchVenue, earnApr*100, benchTS.Format("01-02 15:04"), sigma*100, src, bound, bestTail*100, bestNet*100)
}
