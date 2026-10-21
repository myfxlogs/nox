// dci_sell_high_gate.go —— D-646 sell_high 提案面双闸（covered_call 卡不可
// 「无券+无 strike」出世，#1687 病灶根修：proposed/viable 33.91% 而 okx 三监测
// 面零 BTC=执行即裸 call 违 yield_on_holdings 前提；params 无 strike/tenor
// 使 σ 尾险界无从核）。
// 持仓闸：venue 可动券=托管池份额行（personal_positions asset=BTC kind=other
// OPEN——D-191 池表象口径）×custody_share@<venue>（D-644 venue 粒度读面），
// <minQty（app_config `sell_high_min_qty`，缺省 0.01=okx dcd 最小申购档；判据
// 落表禁硬编码——代码缺省仅兜底）→ 不产提案。
// 结构闸：σ 界内选档 net(prem−tail)>0 且 strike/tenor/product_ref 三字段齐；
// MinAmount 在案时券须覆盖申购档。双闸皆 fail-closed——读败/σ 缺/约束缺/
// 报价缺=不产提案不臆放（dciPremiumGate 同款纪律）。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// sellHighMinQtyFloor 券下限代码兜底（判据源=app_config `sell_high_min_qty`，
// 行缺/坏值=okx dcd 最小申购档≈0.01 BTC 实证值兜底——xfer.go ok-flag 先例）。
const sellHighMinQtyFloor = 0.01

// sellHighMinQty 券下限读面（判据落表热读；行缺/读败→代码兜底不臆放——
// 读败走 fail-closed 侧语义：闸仍守下限，只是不可调）。
func sellHighMinQty(ctx context.Context, st *pgstore.Store) float64 {
	if v, ok, err := st.AppConfigFloat(ctx, "sell_high_min_qty"); err == nil && ok && v > 0 {
		return v
	}
	return sellHighMinQtyFloor
}

// custodyBTCAvail venue 可动券=托管池份额行合计×venue 粒度 share。
// 份额行缺 qty→0 行不臆补；池行零=如实零（无券不产卡票面语义）。
func custodyBTCAvail(ctx context.Context, st *pgstore.Store, share float64) (float64, error) {
	poss, err := st.ListOpenPersonalPositions(ctx)
	if err != nil {
		return 0, fmt.Errorf("托管池份额行读败：%w", err)
	}
	var pool float64
	for _, p := range poss {
		if p.Asset == "BTC" && p.Kind == "other" && p.Qty != nil {
			pool += *p.Qty
		}
	}
	return pool * share, nil
}

// sellHighGate 双闸判定：返回过闸与否+拒因注记（注记带券/σ/界/净额要素可审计）。
func sellHighGate(ctx context.Context, st *pgstore.Store, venue string) (bool, string) {
	cons, cerr := st.ListCapabilityConstraints(ctx)
	if cerr != nil {
		return false, fmt.Sprintf("约束表读败：%v", cerr)
	}
	k, tmax, ok := gate.SigmaTailParams(cons, "CALL")
	if !ok {
		return false, "尾险约束缺（dci_call_otm_k/dci_tenor_max_days 行——fail-closed 不产提案）"
	}
	share := gate.CustodyShareOf(cons, venue)
	if share == nil {
		return false, fmt.Sprintf("custody_share 约束缺（custody_share@%s 行——券定界不可判 fail-closed）", venue)
	}
	minQty := sellHighMinQty(ctx, st)
	avail, err := custodyBTCAvail(ctx, st, *share)
	if err != nil {
		return false, err.Error()
	}
	// 持仓闸：可动券<下限=无券不产（执行即裸 call——yield_on_holdings 前提破）。
	if avail < minQty {
		return false, fmt.Sprintf("无券：BTC 池份额×share=%.6f<min %.4f（缺持仓——covered_call 前提破不产卡）", avail, minQty)
	}
	// 结构闸：σ 界内达标档 net>0 且结构三字段齐。
	sigma, src, serr := fetchSigma(ctx, st)
	if serr != nil {
		return false, serr.Error()
	}
	spot, err := st.LatestSpreadSpotRef(ctx, "BTC")
	if err != nil || spot <= 0 {
		return false, "BTC 现货参照缺（界不可算不臆放）"
	}
	bound := gate.StrikeBound(spot, gate.SigmaBoundRatio("CALL", sigma, k, tmax))
	qs, err := st.ListDCIQuotes(ctx, venue, "BTC", "CALL", "BTC", int(tmax), bound)
	if err != nil {
		return false, fmt.Sprintf("%s CALL 报价读败：%v", venue, err)
	}
	if len(qs) == 0 {
		return false, fmt.Sprintf("%s CALL 达标档报价缺（σ 界 %.0f 上无档）", venue, bound)
	}
	var best *pgstore.CexDCIQuote
	var bestNet, bestPrem, bestTail float64
	for i := range qs {
		qq := &qs[i]
		if qq.Strike == nil || qq.TenorDays == nil || qq.ProductRef == "" {
			continue // 结构三字段缺一=不可评档跳过（fail-closed 不臆补）
		}
		prem := qq.APY
		if venue == "binance" {
			prem /= 100 // binance apy=%口径归一小数（dciPremiumGate 同式）
		}
		tail := gate.TailCostAnn("CALL", spot, *qq.Strike, sigma, *qq.TenorDays)
		if net := prem - tail; best == nil || net > bestNet {
			best, bestNet, bestPrem, bestTail = qq, net, prem, tail
		}
	}
	if best == nil {
		return false, fmt.Sprintf("%s CALL 达标档 strike/tenor/product_ref 缺（不可评不臆放）", venue)
	}
	if bestNet <= 0 {
		return false, fmt.Sprintf(
			"尾损盖溢价：premium_ann %.2f%%−tail %.2f%%=net %.2f%%≤0（σ=%.2f%% 界=%.0f strike=%.0f tenor=%dd）",
			bestPrem*100, bestTail*100, bestNet*100, sigma*100, bound, *best.Strike, *best.TenorDays)
	}
	// 券覆盖申购档（MinAmount 在案时钉——缺案不臆放沿用 fail-closed 侧）。
	if best.MinAmount != nil && avail < *best.MinAmount {
		return false, fmt.Sprintf("券不足：avail %.6f<申购档 minAmount %.6f（%s）", avail, *best.MinAmount, best.ProductRef)
	}
	return true, fmt.Sprintf("券=%.6f BTC（×share %.2f≥min %.4f）；σ=%.2f%%（%s）界=%.0f strike=%.0f tenor=%dd tail=%.2f%% net=%.2f%%",
		avail, *share, minQty, sigma*100, src, bound, *best.Strike, *best.TenorDays, bestTail*100, bestNet*100)
}

// sellHighGateEnforce 闸+处置件：不过=存量 proposed 翻 deferred 如实（#1687
// 处置——券到自动复产）+dedup 告警留痕；过=注记交 gate_note。
func sellHighGateEnforce(ctx context.Context, st *pgstore.Store, tmpl, venue string) (bool, string) {
	ok, why := sellHighGate(ctx, st, venue)
	if ok {
		return true, why
	}
	if n, derr := st.DeferProposalsByPair(ctx, tmpl, venue); derr == nil && n > 0 {
		log.Printf("[oppmatch] sell_high 闸不过 %s@%s 存量提案翻 deferred %d 行", tmpl, venue, n)
	}
	if aerr := st.InsertAlertDedup(ctx, "info", fmt.Sprintf(
		"sell_high 双闸：%s@%s %s——本拍不产提案（存量 proposed 已翻 deferred）", tmpl, venue, why), 6*time.Hour); aerr != nil {
		log.Printf("[oppmatch] sell_high 闸 alert: %v", aerr)
	}
	return false, why
}

// sellHighStructBelt 结构闸 belt：covered_call params 三字段缺一→卡面强制
// unevaluable 标 struct_missing（viable 须可核 σ 尾险界——无 strike 不翻
// viable；存量无字段卡如 #1687 复评如实降级）。
func sellHighStructBelt(tmpl string, params map[string]any, card *pgstore.OppEval) {
	if tmpl != "dci_sell_high_covered_call" || card == nil || card.Verdict == "unevaluable" {
		return
	}
	var lack []string
	for _, k := range []string{"strike", "tenor_days", "product_ref"} {
		if params[k] == nil {
			lack = append(lack, k)
		}
	}
	if len(lack) > 0 {
		card.Verdict = "unevaluable"
		card.EvidenceRefs["struct_missing"] = strings.Join(lack, ",")
	}
}
