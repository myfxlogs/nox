package main

// diff_divergence.go —— 跨所 funding 分化对纯函数件（原 diff.go 拆分，零行为变）。

import (
	"fmt"
	"math"
	"sort"

	"nox/internal/store/pgstore"
)

// classFilteredKinds D-374 A 面：股权/指数/盘前类 perp 的 funding 语义各所不同（休市冻结/
// 基准不同），跨所数值差不可解释也不可执行 → divergence 压 digest（保留可见性不删）。
// 豁免：COMMODITY（XAU 类 CEX perp funding 24/7 真有价）/FX/COIN/空 class（不能证明是股票）。
var classFilteredKinds = map[string]bool{
	"EQUITY": true, "CN_EQUITY": true, "HK_EQUITY": true, "KR_EQUITY": true,
	"INDEX": true, "PREMARKET": true,
}

// divPair D-376：divergence 对的结构化承载（event 文案/纸面开仓/告警分层共用数据源）。
type divPair struct {
	Canon                 string
	ShortVenue, LongVenue string  // short=funding 高侧（收割方向在键内）
	ShortSym, LongSym     string  // 两侧原始 symbol——纸面重算按 venue|rawSym 查行
	FundShort, FundLong   float64 // 两侧 funding_ann（%/yr）
	NetAnn                float64 // = FundShort−FundLong ≥ divPct
	VolMin                float64
}

// divergences 同 canonical 跨所 funding 分化候选：|ann_i−ann_j|≥divPct 且两侧 vol≥volMinUSD。
// A 面：canonical 分组内任一侧非空 Class ∈ classFilteredKinds → 全部 pair 跳过，
// canonical 计入 classFiltered 返回（digest 汇总渲染）；双侧矛盾取非空侧=宁可信其有。
// 重构为 divPair 返回（event 文案/纸面开仓/分层分流共用源）——canonical 分组与
// class 过滤逻辑逐字节保留；short=funding 高侧（NetAnn 恒正），event 渲染走 divEvent。
func divergences(rows []normRow, divPct, volMinUSD float64) (pairs []divPair, classFiltered []string) {
	byCanon := make(map[string][]normRow)
	for _, n := range rows {
		if n.FundingAnn != nil && n.Vol24h >= volMinUSD {
			byCanon[n.Canonical] = append(byCanon[n.Canonical], n)
		}
	}
	for canon, rs := range byCanon {
		filtered := false
		for _, r := range rs {
			if classFilteredKinds[r.Class] {
				filtered = true
				break
			}
		}
		if filtered {
			classFiltered = append(classFiltered, canon)
			continue
		}
		for i := 0; i < len(rs); i++ {
			for j := i + 1; j < len(rs); j++ {
				a, b := rs[i], rs[j]
				if a.Venue == b.Venue {
					continue
				}
				sh, lo := a, b // short=funding 高侧（确定性排序：净差≥divPct 排除等值歧义）
				if *b.FundingAnn > *a.FundingAnn {
					sh, lo = b, a
				}
				if math.Abs(*a.FundingAnn-*b.FundingAnn) >= divPct {
					pairs = append(pairs, divPair{
						Canon:      canon,
						ShortVenue: sh.Venue, LongVenue: lo.Venue,
						ShortSym: sh.Symbol, LongSym: lo.Symbol,
						FundShort: *sh.FundingAnn, FundLong: *lo.FundingAnn,
						NetAnn: *sh.FundingAnn - *lo.FundingAnn,
						VolMin: math.Min(sh.Vol24h, lo.Vol24h),
					})
				}
			}
		}
	}
	sort.Strings(classFiltered) // map 迭代无序 → digest 输出确定
	return pairs, classFiltered
}

// divEvent D-376 S1：pair → FUNDING_DIVERGENCE 事件（文案格式与旧径逐字同型——
// Short:Long 在前，旧为所名字母序，现为 funding 高侧序；watchlist 解析取两侧不依赖顺序）。
func divEvent(p divPair) pgstore.CexEvent {
	return pgstore.CexEvent{
		Venue: "multi", Symbol: p.Canon, EventType: "FUNDING_DIVERGENCE",
		Detail: fmt.Sprintf("%s:%s %+.1f%% vs %+.1f%% ann", p.ShortVenue, p.LongVenue, p.FundShort, p.FundLong),
	}
}
