// quote_sources.go —— D-637 序②：quoteFor 三模板报价源（诚实 gross 现值面——
// 无源=ok=false unevaluable，fail-closed 禁凑数估）。
package oppmatch

import (
	"context"

	"time"

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

// term_basis 流动性/期限阈（app_config 键覆盖——禁字面量硬线，默认值=实施裁
// 小阈防死价行：vol24h=0 陈价合约=伪基差病灶实证〔okx XAU-USD_UM-261030
// ann=43.5 vol24h=0 挂 binance 名出 #1938〕；min_tenor=近到期剔除）。
const (
	termBasisMinVol24h    = 1000.0 // app_config: term_basis_min_vol24h
	termBasisMinTenorDays = 14.0   // app_config: term_basis_min_tenor_days
)

// termBasisThresholds 阈读（app_config 键优先生效——配置面非字面量硬线）。
func termBasisThresholds(ctx context.Context, st *pgstore.Store) (minVol, minTenor float64) {
	minVol, minTenor = termBasisMinVol24h, termBasisMinTenorDays
	if v, ok, err := st.AppConfigFloat(ctx, "term_basis_min_vol24h"); err == nil && ok {
		minVol = v
	}
	if v, ok, err := st.AppConfigFloat(ctx, "term_basis_min_tenor_days"); err == nil && ok {
		minTenor = v
	}
	return
}

// termBasisBest venue 过滤后基差最优行（#1938 伪 APR 病灶三伤修）：
// ①venue==提案 venue（okx 合约不挂 binance 名）；②Vol24h 流动性地板
// （nil/低于阈剔——陈价零成交合约伪基差不入围）；③近到期剔除
// （expiry−now<minTenor 剔——到期挤压期价非真实基差面）。
// 返回 *CexUniverseRow——提案 params 须能具名买什么合约（symbol+expiry）。
func termBasisBest(ctx context.Context, st *pgstore.Store, venue string) (*pgstore.CexUniverseRow, error) {
	rows, err := st.ListCexUniverse(ctx)
	if err != nil {
		return nil, err
	}
	minVol, minTenor := termBasisThresholds(ctx, st)
	now := time.Now()
	var best *pgstore.CexUniverseRow
	for k := range rows {
		r := rows[k]
		if r.Venue != venue || r.ContractType != "FUTURES" || r.BasisAnn == nil {
			continue
		}
		if r.Vol24h == nil || *r.Vol24h < minVol {
			continue // 流动性地板——零成交陈价伪基差剔
		}
		if r.ExpiryTS != nil && r.ExpiryTS.Sub(now) < time.Duration(minTenor*24)*time.Hour {
			continue // 近到期剔除——8 天到期合约实证病灶面
		}
		if best == nil || *r.BasisAnn > *best.BasisAnn {
			cp := r // map 元素不可取址——拷贝具名行回传
			best = &cp
		}
	}
	return best, nil
}

// termBasisQuote 期限基差年化现值——venue 过滤后最优行 BasisAnn（预计算列
// 采集层实算禁二次估值；无合格合约=ok=false unevaluable 诚实缺非造零）。
func termBasisQuote(ctx context.Context, st *pgstore.Store, venue string) (float64, bool, error) {
	best, err := termBasisBest(ctx, st, venue)
	if err != nil || best == nil {
		return 0, false, err
	}
	return *best.BasisAnn, true, nil
}
