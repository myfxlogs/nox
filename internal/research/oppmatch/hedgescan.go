// hedgescan.go —— 对冲腿可行面扫描（approved=0 死结正解）：cfd_swap 裸腿假设逐条
// 查同标的 CEX 对冲腿——资产桥走 asset_equiv 类（canonical→asset_class→CEX canonical
// →universe.base 剥 quote 后缀实读），binance/okx/gate 现货+perp 目录库内实读不臆造。
// 有腿→hedged 形态假设行（category_key 含双腿幂等）；无腿→原行 detail 附
// 「对冲腿：无」注记不删假设。只读扫描零写 venue；funding 用 universe 实读值；
// 产出仍是假设级不进提案。
package oppmatch

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"nox/internal/store/pgstore"
)

// hedgeScan 裸腿假设对冲可行面扫描（RunOnce swapScan 尾挂——在其覆写 detail 后
// 再注记，幂等结论每拍重放）。
func hedgeScan(ctx context.Context, st *pgstore.Store) {
	hyps, err := st.ListSwapHypotheses(ctx)
	if err != nil {
		log.Printf("[oppmatch] hedge scan list: %v", err)
		return
	}
	var newKeys []string
	for _, h := range hyps {
		parts := strings.Split(h.CategoryKey, "|")
		if len(parts) != 4 {
			continue
		}
		mtVenue, sym, side := parts[1], parts[2], parts[3]
		// CFD 符号→broker_symbol_map canonical→asset_class（同标等价类桥）；
		// 桥任一环节缺=同标腿不可证→按「无腿」注记（如实不删假设）。
		canon, err := st.BrokerSymbolCanonical(ctx, mtVenue, sym)
		if err != nil {
			continue
		}
		class := ""
		if canon != "" {
			var cerr error
			if class, cerr = st.AssetClassOf(ctx, canon); cerr != nil {
				continue
			}
		}
		if canon == "" || class == "" {
			if err := st.MarkHypothesisNoHedge(ctx, h.ID); err != nil {
				log.Printf("[oppmatch] hedge mark %s: %v", h.CategoryKey, err)
			}
			continue
		}
		legs, err := st.HedgeLegsForClass(ctx, class)
		if err != nil {
			log.Printf("[oppmatch] hedge legs %s: %v", class, err)
			continue
		}
		// 方向对向腿：CFD short 收息→CEX long（现货零成本/ perp 付 funding）；
		// CFD long 收息→CEX short（现货不能空→只留 perp，funding>0 时空方收息=负成本）。
		var cand []pgstore.HedgeLegRow
		for _, l := range legs {
			if side == "long" && l.Spot {
				continue
			}
			cand = append(cand, l)
		}
		if len(cand) == 0 {
			if err := st.MarkHypothesisNoHedge(ctx, h.ID); err != nil {
				log.Printf("[oppmatch] hedge mark %s: %v", h.CategoryKey, err)
			}
			continue
		}
		sort.Slice(cand, func(i, j int) bool {
			if cand[i].Spot != cand[j].Spot {
				return cand[i].Spot // 现货腿零息成本优先
			}
			if side == "short" {
				return cand[i].FundingAnn < cand[j].FundingAnn // long 腿付息低者优
			}
			return cand[i].FundingAnn > cand[j].FundingAnn // short 腿收息高者优
		})
		if len(cand) > 3 {
			cand = cand[:3]
		}
		swapAnn := swapAnnOf(ctx, st, mtVenue, sym, side)
		for _, l := range cand {
			key := fmt.Sprintf("cfd_swap_hedged|%s|%s|%s|%s|%s",
				mtVenue, sym, side, l.Venue, l.Symbol)
			cost := 0.0 // 现货腿零息成本
			if !l.Spot {
				cost = l.FundingAnn
				if side == "long" {
					cost = -cost // 空腿收息=负成本
				}
			}
			_, inserted, _, uerr := st.UpsertHypothesis(ctx, pgstore.OppHypothesis{
				Venue: mtVenue, CategoryKey: key,
				InstType: "cfd_hedged", Shape: "swap_hedge",
				Sample: fmt.Sprintf("CFD %s %s@%s + %s %s@%s",
					side, sym, mtVenue, hedgeSide(side), l.Symbol, l.Venue),
				Detail: fmt.Sprintf(
					"腿对：%s %s@%s × %s %s@%s（%s）；名义 1:1 同标的 qty 换算假设；"+
						"swap≈%.1f%%/yr − 对冲腿成本≈%.1f%%/yr（universe funding 实读）→净 carry≈%.1f%%/yr",
					side, sym, mtVenue, hedgeSide(side), l.Symbol, l.Venue, l.ContractType,
					swapAnn, cost, swapAnn-cost),
			})
			if uerr != nil {
				log.Printf("[oppmatch] hedge hypothesis %s: %v", key, uerr)
				continue
			}
			if inserted {
				newKeys = append(newKeys, fmt.Sprintf("%s↔%s@%s", sym, l.Symbol, l.Venue))
			}
		}
	}
	if len(newKeys) > 0 {
		sort.Strings(newKeys)
		if err := st.InsertAlert(ctx, nil, "info", fmt.Sprintf(
			"对冲腿可行面新假设 %d 条：%s（CFD 收息×CEX 对向腿自含结构——假设级不进提案）",
			len(newKeys), strings.Join(newKeys, "；"))); err != nil {
			log.Printf("[oppmatch] hedge alert: %v", err)
		}
	}
}

func hedgeSide(cfdSide string) string {
	if cfdSide == "short" {
		return "long"
	}
	return "short"
}

// swapAnnOf 裸腿假设的 swap 年化重取（facts 最新行实读，缺=0 如实）。
func swapAnnOf(ctx context.Context, st *pgstore.Store, venue, sym, side string) float64 {
	kind := "swap_long"
	if side == "short" {
		kind = "swap_short"
	}
	bps, err := st.LatestFactValue(ctx, kind, venue, sym)
	if err != nil || bps <= 0 {
		return 0
	}
	return bps * 365 / 100
}
