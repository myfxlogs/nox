// run_venue.go —— earn 提案 venue 枚举+快照参数（自 run.go 拆出保 ≤300 行门禁；纯搬移零语义改）。
package oppmatch

import (
	"context"
	"fmt"
	"log"

	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// earnQuoteVenues earn_apr_lending 提案 venue 枚举：rail 覆盖集内有 USDT
// 报价行的 venue 各出独立提案（报价所取 venue 如实写提案 venue——旧单 binance
// venue+跨所顶档价=「okx 利率被 binance 轨执行」错 venue 错价面）。报价读败
// venue 本拍不产（读败不臆造不静默——log 留痕）。
func earnQuoteVenues(ctx context.Context, st *pgstore.Store) ([]string, map[string]float64) {
	var venues []string
	aprs := map[string]float64{}
	for _, v := range sortedVenues(gate.EarnExecVenues) {
		apr, ok, err := st.LatestEarnAPR(ctx, v, "USDT")
		if err != nil {
			log.Printf("[oppmatch] earn_apr %s 报价读败（本 venue 不产）: %v", v, err)
			continue
		}
		if ok {
			venues = append(venues, v)
			aprs[v] = apr
		}
	}
	return venues, aprs
}

// applyDciSel dci 脱节修：expected_apr/eval 卡 gross=闸实选档 venue 原生 apy
// （非 MaxDCIAPY 全表 30d 极值——#1827 卡 33.9% vs 实买 8.57% 病灶）。
func applyDciSel(rv *Result, dsel *dciSel) {
	if dsel == nil {
		return
	}
	rv.APR = &dsel.prem
	rv.GrossEvidence = fmt.Sprintf("fin_cex_dci_quote %s（闸实选档）", dsel.row.ProductRef)
}

// proposalParams 提案快照参数（审计锚非钉价）：dci 模板补最优档 product/strike/tenor
// 快照；其余仅 apr——执行时实时闸复验为准（禁区：禁按提案时 strike 硬执行）。
func proposalParams(ctx context.Context, st *pgstore.Store, r Result, venue string, dsel *dciSel) map[string]any {
	p := map[string]any{"venue": venue}
	if r.APR != nil {
		p["apr"] = *r.APR
	}
	// dci 脱节修：闸实选行三字段/apr 直接钉（快照与闸判定同档——
	// qs[0] 重查会取异档；执行时实时闸复验径不变）。
	if dsel != nil {
		p["apr"] = dsel.prem
		p["product_ref"] = dsel.row.ProductRef
		if dsel.row.Strike != nil {
			p["strike"] = *dsel.row.Strike
		}
		if dsel.row.TenorDays != nil {
			p["tenor_days"] = *dsel.row.TenorDays
		}
		return p
	}
	var opt, inv string
	switch r.TemplateID {
	case "dci_sell_high_covered_call":
		opt, inv = "CALL", "BTC"
	case "dci_buy_low":
		opt, inv = "PUT", "USDT"
	}
	if opt != "" {
		// 快照同界——bound=现货参照×σ 驱动界（D-526；σ 读败→0 fail-closed
		// 无界快照降级；快照=审计锚非钉价，执行时实时闸复验不变）。
		sigma, _, _ := fetchSigma(ctx, st)
		bound := dciStrikeBound(ctx, st, opt, sigma)
		if qs, err := st.ListDCIQuotes(ctx, venue, "BTC", opt, inv, 90, bound); err == nil && len(qs) > 0 {
			p["product_ref"] = qs[0].ProductRef
			if qs[0].Strike != nil {
				p["strike"] = *qs[0].Strike
			}
			if qs[0].TenorDays != nil {
				p["tenor_days"] = *qs[0].TenorDays
			}
		}
	}
	return p
}
