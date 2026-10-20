// quote_sources.go —— D-637 序②：quoteFor 三模板报价源（诚实 gross 现值面——
// 无源=ok=false unevaluable，fail-closed 禁凑数估）。
package oppmatch

import (
	"context"

	"nox/internal/store/pgstore"
)

// cfdSwapCarryQuote CFD swap 收息 − CEX funding 对冲成本同源现值（facts 最新行——
// swapAnn=bps×365/100 年化〔hedgescan 同径〕；成本=同 canonical CEX funding max(0,·)
// 保守〔负 funding 红利不计〕；canonical 映射缺/funding 缺=该行跳过 fail-closed）。
func cfdSwapCarryQuote(ctx context.Context, st *pgstore.Store) (float64, bool, error) {
	// funding 全表一次建 canonical→最低对冲成本 map（负 funding 红利不计——max(0,·)）。
	funds, err := st.LatestFactsByKind(ctx, "funding")
	if err != nil {
		return 0, false, err
	}
	fundMin := map[string]float64{}
	for _, f := range funds {
		if f.Value <= 0 {
			continue
		}
		if cur, ok := fundMin[f.Symbol]; !ok || f.Value < cur {
			fundMin[f.Symbol] = f.Value
		}
	}
	var best float64
	var anyOK bool
	for _, kind := range []string{"swap_long", "swap_short"} {
		rows, err := st.LatestFactsByKind(ctx, kind)
		if err != nil {
			return 0, false, err
		}
		for _, f := range rows {
			if f.Value <= 0 {
				continue
			}
			canon, err := st.BrokerSymbolCanonical(ctx, f.Venue, f.Symbol)
			if err != nil || canon == "" {
				continue
			}
			cost, ok2 := fundMin[canon]
			if !ok2 {
				continue // canonical 无 funding 配对=对冲成本缺证 fail-closed（非零成本放行）
			}
			if net := f.Value*365/100 - cost; net > best {
				best, anyOK = net, true
			}
		}
	}
	return best, anyOK, nil
}

// crossVenueSpotQuote 跨所价差现值——fin_cex_spread 每 symbol 最新行
// spread_bps/100=percent（单周转如实标不年化；无行=ok=false unevaluable）。
func crossVenueSpotQuote(ctx context.Context, st *pgstore.Store) (float64, bool, error) {
	rows, err := st.LatestCexSpreads(ctx)
	if err != nil {
		return 0, false, err
	}
	var best float64
	var ok bool
	for _, r := range rows {
		if r.SpreadBps > 0 && r.SpreadBps/100 > best {
			best, ok = r.SpreadBps/100, true
		}
	}
	return best, ok, nil
}

// termBasisQuote 期限基差年化现值——fin_cex_universe FUTURES 行 last_basis_ann
// 已有采集面直接取 MAX（precomputed 列=采集层实算，禁二次估值）。
func termBasisQuote(ctx context.Context, st *pgstore.Store) (float64, bool, error) {
	rows, err := st.ListCexUniverse(ctx)
	if err != nil {
		return 0, false, err
	}
	var best float64
	var ok bool
	for _, r := range rows {
		if r.ContractType != "FUTURES" || r.BasisAnn == nil {
			continue
		}
		if *r.BasisAnn > best {
			best, ok = *r.BasisAnn, true
		}
	}
	return best, ok, nil
}
