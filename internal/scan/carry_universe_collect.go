// carry_universe_collect.go —— carry 宇宙扫描器 MT swap fact 收集（D-451 S3：
// 自 carry_universe.go Run() 提取，纯搬家零语义变更；D-412 F1-F4/D-382/R1 注释保留）。
package scan

import (
	"context"
	"time"

	"arb/internal/fact"
)

// mtSwap MT swap 候选腿（long/short 共用）。
// MT swap facts D-412 F1/F2/F3：单位归一（bps_daily×365/100→%/yr 单一换算点）+
// venue 全保留（F2 全叉积：归并键=canonical|mt_venue，同 canonical 多 venue 不再坍缩
// 取最优——授权在跑线必须恒在监控视野，D-410 保底槽三重键前提）+ 7d 窗口均值取值
// （F3 抗 flap：swap_free=|均值年化|<1%；点值判定=抛硬币 39050818 实证）。
type mtSwap struct {
	venue, canonical string
	symbol           string  // broker 裸符号（D-465：mt×mt 轨取 tick 用）
	val              float64 // 年化 %/yr（F1 换算后；F3 起为 7d 均值年化）
	ts               time.Time
	stale            bool // F4：fact 龄期超闸（>UniverseSwapMaxAge）
}

// collectMtSwaps D-451 S3：Run() 内两同构循环提取——long/short 只差 fact 列表、
// 值谓词方向（long 收息=负值候选 / short 付息=负值排除）、kind（swap_long/swap_short）、
// modeAllowed 方向参。filtered/res.Skipped 为副作用参数（语义随原版保留）。
func (sc *CarryUniverseScanner) collectMtSwaps(ctx context.Context, long, short []fact.Fact,
	tmKnown func(fact.Fact) (int64, bool), filtered map[string]bool, res *ScanResult,
	aliases map[string]string,
) (map[string]mtSwap, map[string]mtSwap) {
	collect := func(facts []fact.Fact, kind string, isLong bool) map[string]mtSwap {
		out := map[string]mtSwap{}
		for _, f := range facts {
			if f.Symbol == "" || (isLong && f.Value > 0) || (!isLong && f.Value < 0) {
				continue // long：正值=收息（多 CFD 不付息不可能）非候选；short：负值=付息非候选；0=免息腿（swap_free 候选保留）
			}
			maps, err := sc.store.ListSymbolMappings(ctx, f.Venue)
			if err != nil {
				continue
			}
			canonical := ""
			for _, m := range maps {
				if m.BrokerSymbol == f.Symbol && m.Canonical != "" {
					canonical = NormalizeJoinKey(m.Canonical) // D-336R1：双侧规范化后入键
					break
				}
			}
			// D-464 S1：别名双侧——映射 canonical 过别名；map miss 时仅别名命中才可直配
			// （裸归一 raw symbol 不算映射线索，与 symdig 分类器同口径）。
			canonical = applyAlias(canonical, aliases)
			if canonical == "" {
				if v, ok := aliases[NormalizeJoinKey(f.Symbol)]; ok {
					canonical = v
				}
			}
			if canonical == "" {
				res.Skipped++
				continue
			}
			// D-382 S1：可执行性过滤——买腿要求 mode∈{LongOnly,Full}，卖腿 {ShortOnly,Full}；
			// 已知且禁 → skip 该 fact（fact 级过滤=最优 venue 被禁时归并自然落次优可交易 venue，canonical 存活不逃脱）。
			if mode, known := tmKnown(f); known && !modeAllowed(mode, isLong) {
				filtered[canonical] = true
				continue
			}
			mean, err := sc.store.FactsWindowStats(ctx, kind, f.Venue, f.Symbol, swapWindowTTL)
			if err != nil {
				continue // F3：窗口零样本/读失败=不产行（既有 fail-closed 语义不变）
			}
			out[canonical+"|"+f.Venue] = mtSwap{
				venue: f.Venue, canonical: canonical, symbol: f.Symbol, val: mean * 365 / 100, ts: f.Ts,
				stale: sc.swapMaxAge > 0 && time.Since(f.Ts) > sc.swapMaxAge, // R1：swap 侧用 swap 闸（48h），修复误用 funding 闸
			}
		}
		return out
	}
	return collect(long, "swap_long", true), collect(short, "swap_short", false)
}
