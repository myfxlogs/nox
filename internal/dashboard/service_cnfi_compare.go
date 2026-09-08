package dashboard

// CN-FI D-247 P1 S4 + D-259 P2 S3：候选池对比（服务端计算，D-043；层内排名事实序，N 逐行）。
// 数学=collect/cnfi 的 LJJZ 口径（IntervalReturn/MaxDrawdown/SliceByDate/HasCaliberBreak 同一定义）。
// passes_screen D-259：state 查询驱动（非硬编码 true）——基金未处于①underperf/②层 dd 破位 → true。

import (
	"context"
	"fmt"
	"sort"
	"time"

	"arb/internal/collect/cnfi"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// RankByReturnDesc 按收益率降序名次（并列=先到先得，稳定序；N=参与排名数）。
type RankedItem struct {
	Idx  int // 原切片下标
	Rank int
	N    int
}

func RankByReturnDesc[T any](items []T, ret func(i int) float64) []RankedItem {
	idx := make([]int, len(items))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return ret(idx[a]) > ret(idx[b]) })
	out := make([]RankedItem, len(items))
	for pos, id := range idx {
		out[id] = RankedItem{Idx: id, Rank: pos + 1, N: len(items)}
	}
	return out
}

// buildCNFICompare D-247 S4 + D-259 S3：候选池对比（每层）。区间=7d/1m/3m/1y；跨断点/缺样本 → 指标缺失。
// passes_screen D-259：查询 cnfi_signal_state（7 天内 active=预警态）驱动，v1 恒 true 退役。
func buildCNFICompare(ctx context.Context, s *Service, now time.Time) ([]*v1.CnfiLayerCompare, error) {
	cands, err := s.store.ListCNFICandidates(ctx)
	if err != nil {
		return nil, err
	}
	// D-259 S3：passes_screen state 驱动（7 天内 active signal = 预警态）
	activeSignals, err := s.store.CNFIActiveSignalKeys(ctx, 7)
	if err != nil {
		return nil, fmt.Errorf("cnfi compare: active signals: %w", err)
	}
	nowS := now.Format("2006-01-02")
	windows := []struct {
		key  string
		from time.Time
	}{
		{"ret_7d", now.AddDate(0, 0, -7)},
		{"ret_1m", now.AddDate(0, -1, 0)},
		{"ret_3m", now.AddDate(0, -3, 0)},
		{"ret_1y", now.AddDate(-1, 0, 0)},
	}
	ddFrom := now.AddDate(0, -3, 0) // 3m 回撤

	byLayer := map[string][]*v1.CnfiFundCompare{}
	for _, cand := range cands {
		series, err := s.store.CNFINAVSeries(ctx, cand.Code)
		if err != nil {
			return nil, err
		}
		pts := make([]cnfi.LJJZPoint, 0, len(series))
		for _, r := range series {
			pts = append(pts, cnfi.LJJZPoint{Date: r.Date, LJJZ: r.LJJZ})
		}
		// D-259 S3：passes_screen = 未处于 ①underperf 或 ②所在层 dd 破位状态
		underperfKey := fmt.Sprintf("fund_underperf_%s", cand.Code)
		layerDDKey := fmt.Sprintf("layer_dd_%s", cand.Layer)
		passes := !activeSignals[underperfKey] && !activeSignals[layerDDKey]
		fc := &v1.CnfiFundCompare{Code: cand.Code, Name: cand.Name, PassesScreen: passes}
		if cand.BreakDate != nil {
			b := true
			fc.IsBreak = &b
		}
		rets := map[string]*float64{}
		for _, w := range windows {
			slice := cnfi.SliceByDate(pts, w.from.Format("2006-01-02"), nowS)
			// R1：跨断点区间 → 指标缺失（不做 −23% 毒值）
			if cand.BreakDate != nil && cnfi.HasCaliberBreak(slice, w.from.Format("2006-01-02"), nowS, *cand.BreakDate) {
				continue
			}
			ret := cnfi.IntervalReturn(slice)
			if ret != nil {
				rets[w.key] = ret
			}
		}
		if v, ok := rets["ret_7d"]; ok {
			fc.Ret_7D = v
		}
		if v, ok := rets["ret_1m"]; ok {
			fc.Ret_1M = v
		}
		if v, ok := rets["ret_3m"]; ok {
			fc.Ret_3M = v
		}
		if v, ok := rets["ret_1y"]; ok {
			fc.Ret_1Y = v
		}
		ddSlice := cnfi.SliceByDate(pts, ddFrom.Format("2006-01-02"), nowS)
		if cand.BreakDate == nil || !cnfi.HasCaliberBreak(ddSlice, ddFrom.Format("2006-01-02"), nowS, *cand.BreakDate) {
			if dd := cnfi.MaxDrawdown(ddSlice); dd != nil {
				fc.MaxDrawdown_3M = dd
			}
		}
		byLayer[cand.Layer] = append(byLayer[cand.Layer], fc)
	}

	// 层内排名（按 ret_1y，缺失回退 ret_3m；两者皆缺不参与排名、N 不计）。
	var layers []*v1.CnfiLayerCompare
	for _, layer := range []string{"liquidity", "coupon", "locked", "opportunity"} {
		funds := byLayer[layer]
		lc := &v1.CnfiLayerCompare{Layer: layer}
		retOf := func(i int) float64 {
			if funds[i].Ret_1Y != nil {
				return *funds[i].Ret_1Y
			}
			if funds[i].Ret_3M != nil {
				return *funds[i].Ret_3M
			}
			return -1e18 // 排最后
		}
		// R-F1：N=参与排名数（Ret_1Y 或 Ret_3M 非nil），缺失指标不计入——
		// -1e18 殿后布局下参与者名次恰为参与者内名次（语义保持，最小 diff）。
		participants := int32(0)
		for _, f := range funds {
			if f.Ret_1Y != nil || f.Ret_3M != nil {
				participants++
			}
		}
		ranked := RankByReturnDesc(funds, retOf)
		for _, rk := range ranked {
			f := funds[rk.Idx]
			// 两者皆缺（无任何区间收益）→ 不参与排名，rank/N 双 nil
			if f.Ret_1Y == nil && f.Ret_3M == nil {
				f.Rank = nil
				f.N = nil
			} else {
				rank, n := int32(rk.Rank), participants
				f.Rank = &rank
				f.N = &n
			}
			lc.Funds = append(lc.Funds, f)
		}
		layers = append(layers, lc)
	}
	return layers, nil
}
