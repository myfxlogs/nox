// run_venue.go —— earn 提案 venue 枚举+快照参数（自 run.go 拆出保 ≤300 行门禁；纯搬移零语义改）。
package oppmatch

import (
	"context"
	"fmt"
	"log"

	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// execVenueSets D-651：venue 可执行面=fin_capability_constraint exec_venue:
// 投影（DB 驱动）；表空/读败回落 gate 静态集（迁移未至不臆断）。
func execVenueSets(ctx context.Context, st *pgstore.Store) map[string]map[string]bool {
	if rows, err := st.ExecVenueSets(ctx); err == nil && len(rows) > 0 {
		return rows
	}
	return gate.ExecTemplateVenueSets()
}

// execVenueList 模板执行轨 venue 集（投影优先——空回落静态集）。
func execVenueList(ctx context.Context, st *pgstore.Store, tid string) []string {
	if m, ok := execVenueSets(ctx, st)[tid]; ok {
		return sortedVenues(m)
	}
	// 投影无该模板行（未种子/新模板）→静态集兜底同族；再缺=nil 交调方。
	if m, ok := gate.ExecTemplateVenueSets()[tid]; ok {
		return sortedVenues(m)
	}
	return nil
}

// earnPreMinSub earn 前提闸最低可申增量（USD）：spotFree<阈=无可申增量
// 不产提案（10-18 决策注记⑦——#836/837/895 前提陈旧病灶：venue 已部署
// +spot 可动≈0 产提案=decline 噪音面）。闸只挡「无增量可申」不碰 APR 面。
const earnPreMinSub = 10.0

// earnIncrementOK earn 族提案前提闸（earn_apr_lending/earn_rotation）：
// spotFree≥earnPreMinSub 且 spot 可动未全入 earn（增量=spot 现值——earn 在
// 息不减可申域）。判定输入=CapitalPoolSnapshots 现值读面（非新源）；读败
// =如实过闸（不臆阻——前提缺失不新增拒绝径）。
func earnIncrementOK(ctx context.Context, st *pgstore.Store, venue string) (bool, string) {
	pools, err := st.CapitalPoolSnapshots(ctx)
	if err != nil {
		return true, fmt.Sprintf("池快照读败过闸: %v", err)
	}
	var spotFree, earnUSD float64
	for _, p := range pools {
		if p.Venue != venue || p.Stale || p.Ghost {
			continue
		}
		switch p.VenueType {
		case "spot":
			if idleGapUSDish(p.Currency) {
				spotFree += p.Equity
			}
		case "earn":
			if idleGapUSDish(p.Currency) {
				earnUSD += p.Equity
			}
		}
	}
	if spotFree < earnPreMinSub {
		return false, fmt.Sprintf("spotFree $%.2f<min_subscribe $%.0f（earn 在息 $%.2f——无可申增量）",
			spotFree, earnPreMinSub, earnUSD)
	}
	return true, ""
}

// earnPreGate earn 族提案前提闸入口（10-18 注记⑦）：earn 族且无可申增量
// →false 不产提案+log；非 earn 族/读败如实过（前提缺失不新增拒绝径）。
func earnPreGate(ctx context.Context, st *pgstore.Store, tid, venue string) bool {
	if !gate.EarnTemplates[tid] {
		return true
	}
	ok, why := earnIncrementOK(ctx, st, venue)
	if !ok {
		log.Printf("[oppmatch] earn 前提闸 %s@%s 不产: %s", tid, venue, why)
	}
	return ok
}

// earnQuoteVenues earn_apr_lending 提案 venue 枚举：rail 覆盖集内有 USDT
// 报价行的 venue 各出独立提案（报价所取 venue 如实写提案 venue——旧单 binance
// venue+跨所顶档价=「okx 利率被 binance 轨执行」错 venue 错价面）。报价读败
// venue 本拍不产（读败不臆造不静默——log 留痕）。
func earnQuoteVenues(ctx context.Context, st *pgstore.Store) ([]string, map[string]float64) {
	var venues []string
	aprs := map[string]float64{}
	for _, v := range execVenueList(ctx, st, "earn_apr_lending") {
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
	if r.TemplateID == "term_basis" {
		// 提案具名合约（#1938 修②）：venue 域实选行 symbol+expiry——执行面/评估
		// 卡须能说出买什么（与 termBasisApplyVenue 同查询径同选）。
		if best, berr := termBasisBest(ctx, st, venue); berr == nil && best != nil {
			p["symbol"] = best.Symbol
			if best.ExpiryTS != nil {
				p["expiry"] = best.ExpiryTS.Format("2006-01-02")
			}
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

// termBasisApplyVenue term_basis venue 域实选接线（#1938 修③）：本 venue 合格
// 最优 FUTURES 行→rv.APR=其 BasisAnn+GrossEvidence 具名合约（venue 过滤+流动
// 性地板+近到期剔除已在 termBasisBest）；无合格行=false 本 venue 不产。
func termBasisApplyVenue(ctx context.Context, st *pgstore.Store, venue string, rv *Result) bool {
	best, err := termBasisBest(ctx, st, venue)
	if err != nil {
		log.Printf("[oppmatch] term_basis %s universe 读败（本拍不产）: %v", venue, err)
		return false
	}
	if best == nil {
		return false
	}
	rv.APR = best.BasisAnn
	exp := "无到期日"
	if best.ExpiryTS != nil {
		exp = best.ExpiryTS.Format("2006-01-02")
	}
	rv.GrossEvidence = fmt.Sprintf("fin_cex_universe %s basis_ann（%s 过滤后最优；vol24h/tenor 阈过）", best.Symbol, exp)
	return true
}
