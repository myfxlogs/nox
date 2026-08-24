package adapter

import (
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

// SymbolMap 返回当前 brokerSymbol → canonical 映射副本（ReloadSymbolMap 后取最新）。
func (a *MT5Adapter) SymbolMap() map[string]string {
	if m := a.symMap.Load(); m != nil {
		cp := make(map[string]string, len(*m))
		for k, v := range *m {
			cp[k] = v
		}
		return cp
	}
	return nil
}

// SymbolMap MT4 版（同上）。
func (a *MT4Adapter) SymbolMap() map[string]string {
	if m := a.symMap.Load(); m != nil {
		cp := make(map[string]string, len(*m))
		for k, v := range *m {
			cp[k] = v
		}
		return cp
	}
	return nil
}

// mt5SkipReason 标定跳过原因（F1）：None+非零=矛盾数据，其余=未标定模式。
func mt5SkipReason(brokerSym string, sg *mt5pb.SymGroup) string {
	if sg.GetSwapType() == mt5pb.SwapType_SwapType_SwapNone {
		return brokerSym + ": swap none with nonzero rates"
	}
	return fmt.Sprintf("%s: uncalibrated mode %d", brokerSym, sg.GetSwapType())
}

// swapPollSummary 每轮每 venue 一行汇总（D-060 F1）：skip 路径零日志=探测面隐形收缩（P4）。
func swapPollSummary(src, venue string, emit, emit0 int, skips []string) {
	if len(skips) == 0 {
		log.Printf("[%s:%s] swap poll: emit=%d emit0=%d skip=0", src, venue, emit, emit0)
		return
	}
	log.Printf("[%s:%s] swap poll: emit=%d emit0=%d skip=%d [%s]", src, venue, emit, emit0, len(skips), strings.Join(skips, ", "))
}

// swapUncoveredSummary WP1 未覆盖 SwapType 计数汇总（禁静默）：
// 有未覆盖类型时逐类型打印 `[swap] uncovered SwapType=%d count=%d`（对拍 aggregator-skip 计数纪律）。
// 无未覆盖类型时零日志（探测面全覆盖=无事可报）。
func swapUncoveredSummary(src, venue string, uncovered map[int32]int) {
	if len(uncovered) == 0 {
		return
	}
	// 排序输出（稳定日志序）
	keys := make([]int32, 0, len(uncovered))
	for k := range uncovered {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	for _, k := range keys {
		log.Printf("[%s:%s] [swap] uncovered SwapType=%d count=%d", src, venue, k, uncovered[k])
	}
}

// swapInPointsGuardUSD D-092 护栏阈值：InPoints 模式每手日 swap 美元额上限。
// WTI/BRENT 现值 $5~94/手/日，阈值取 5~10 倍余量。超限 → warn 日志仍 emit
// （异常可见不静默，防 FxPro 未来将 mode 9 改语义导致 emit 值失真）。
const swapInPointsGuardUSD = 500.0

// swapInPointsGuardRail D-092：InPoints emit 后护栏——每手日 swap 美元额
// |s|×point×CS 超阈值时记 warn（仍 emit）。返回 true=已告警（测试可断言）。
func swapInPointsGuardRail(src, venue, brokerSym string, s, point, cs float64) bool {
	if cs <= 0 || point <= 0 {
		return false
	}
	perLot := math.Abs(s) * point * cs
	if perLot > swapInPointsGuardUSD {
		log.Printf("[%s:%s] [swap] WARN in-points per-lot USD=%.2f > %.0f (sym=%s s=%.4f point=%.6f cs=%.0f) — emit retained, possible semantic drift",
			src, venue, perLot, swapInPointsGuardUSD, brokerSym, s, point, cs)
		return true
	}
	return false
}

// weekdayNames Go time.Weekday → 中文名（日志可读，D-077 V2）。
var weekdayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// collectTripleSwapDayFacts 三倍日 per-symbol 全量收集（D-077 修复⑪A M3）。
// 同 venue 值一致 → 发 venue 级 Fact（symbol=venue）；不一致 → 发 per-symbol Fact + warn。
// 空 map → 不发（全失败，下游 fail-closed 跳过）。
func collectTripleSwapDayFacts(src, venue string, tripleDays map[string]float64) []fact.Fact {
	if len(tripleDays) == 0 {
		return nil
	}
	// 检查一致性
	first := -1.0
	for _, d := range tripleDays {
		first = d
		break
	}
	allSame := true
	for _, d := range tripleDays {
		if d != first {
			allSame = false
			break
		}
	}
	if allSame {
		// 一致 → venue 级 Fact（现行为）
		logTripleSwapDay(src, venue, first)
		return []fact.Fact{tripleSwapDayFact(venue, venue, first, src)}
	}
	// 不一致 → per-symbol Fact + warn 差异告警
	log.Printf("[%s:%s] [swap] triple swap day per-symbol differs (archive 实证坐实):", src, venue)
	var out []fact.Fact
	for canon, d := range tripleDays {
		name := "Unknown"
		if int(d) >= 0 && int(d) < len(weekdayNames) {
			name = weekdayNames[int(d)]
		}
		log.Printf("[%s:%s] [swap]   %s → %s", src, venue, canon, name)
		out = append(out, tripleSwapDayFact(venue, canon, d, src))
	}
	return out
}

// logTripleSwapDay 日志列出各 venue 实际三倍日（D-077 V2→修复⑪A M3：以本系统实测为准）。
func logTripleSwapDay(src, venue string, weekday float64) {
	name := "Unknown"
	if int(weekday) >= 0 && int(weekday) < len(weekdayNames) {
		name = weekdayNames[int(weekday)]
	}
	log.Printf("[%s:%s] [swap] triple swap day=%s (all symbols consistent)", src, venue, name)
}

// tradeModeFact 构造 trade_mode Fact（D-060 F3）：value=MT5 ENUM_SYMBOL_TRADE_MODE 枚举透传。
func tradeModeFact(venue, canon string, mode mt5pb.TradeMode, src string) fact.Fact {
	return fact.Fact{
		Kind:   fact.KindTradeMode,
		Venue:  venue,
		Symbol: canon,
		Value:  float64(mode),
		Unit:   fact.UnitMode,
		Ts:     time.Now(),
		Src:    src,
	}
}

// marginFact 构造保证金率 Fact（S6）：value=小数比率（0.01=1%）。
// repeated 多档（按 volume tier）取首档（最低 volume 档费率）；
// 多档完整语义（档位划分/后续档费率）未对拍官方文档，留待风控消费时再扩展。
// rates 为空 → 不发（缺字段，调用方判空后不调用）。
func marginFact(kind, venue, canon string, rate float64, src string) fact.Fact {
	return fact.Fact{
		Kind:   kind,
		Venue:  venue,
		Symbol: canon,
		Value:  rate,
		Unit:   fact.UnitRatio,
		Ts:     time.Now(),
		Src:    src,
	}
}

// firstMarginRate 取 repeated margin rate 首档（最低 volume 档费率，S6）。
// 空切片 → ok=false（缺字段不发）。
func firstMarginRate(rates []float64) (float64, bool) {
	if len(rates) == 0 {
		return 0, false
	}
	return rates[0], true
}

// swapFact 构造 swap Fact（符号 canon 为 canonical；Ts 用本地接收时间，与报价流一致）。
func swapFact(kind, venue, canon string, bps float64, src string) fact.Fact {
	return fact.Fact{
		Kind:   kind,
		Venue:  venue,
		Symbol: canon,
		Value:  bps,
		Unit:   fact.UnitBpsDaily,
		Ts:     time.Now(),
		Src:    src,
	}
}

// tripleSwapDayFact 构造三倍 swap 日 Fact（D-077 V2→修复⑪A M3：per-venue 或 per-symbol）。
// weekday 为 Go time.Weekday（0=Sunday...6=Saturday）。
// symbol=venue 名 → venue 级（同 venue 一致时）；symbol=canon → per-symbol（不一致时）。
func tripleSwapDayFact(venue, symbol string, weekday float64, src string) fact.Fact {
	return fact.Fact{
		Kind:   fact.KindTripleSwapDay,
		Venue:  venue,
		Symbol: symbol,
		Value:  weekday,
		Unit:   fact.UnitWeekday,
		Ts:     time.Now(),
		Src:    src,
	}
}

// mt5TripleSwapDay MT5 V3DaysSwap → Go time.Weekday（0=Sunday...6=Saturday，proto 与 Go 同序）。
func mt5TripleSwapDay(v mt5pb.V3DaysSwap) float64 {
	return float64(v)
}

// mt4TripleSwapDay MT4 SwapRollover3Days → Go time.Weekday。
// **无现网实证**：MT4/MT5 同族枚举语义可能不同（MT5 0=Sunday）。
// 假设 MT4 proto: 0=Monday...4=Friday（无周末）；Go: 0=Sunday...6=Saturday。
// 映射假设：MT4 0(Mon)→1, 1(Tue)→2, 2(Wed)→3, 3(Thu)→4, 4(Fri)→5（+1 偏移）。
// **首个 MT4 账户接入时必须实测对拍后核验**（比照 mt4SwapMode 标注风格）。
func mt4TripleSwapDay(v int32) float64 {
	return float64(v + 1)
}

// swapResult 汇总：全失败返回错误（Scheduler 退避重试），部分失败只记日志。
func swapResult(venue string, facts []fact.Fact, errs []string) ([]fact.Fact, error) {
	if len(facts) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("swap poll failed: %s", strings.Join(errs, "; "))
	}
	for _, e := range errs {
		log.Printf("[%s] swap %s", venue, e)
	}
	return facts, nil
}

// symbolSet 提取映射去重后的 canonical 集合。
func symbolSet(sm map[string]string) []string {
	seen := make(map[string]bool, len(sm))
	out := make([]string, 0, len(sm))
	for _, c := range sm {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// venuePrice 取 canonical 在本 venue 的最新 bid；缺失回退任一 venue 中价（近似换算，bps 精度足够）。
func venuePrice(snap map[string]map[string]bus.Quote, canonical, venue, venueType string) float64 {
	venues, ok := snap[canonical]
	if !ok {
		return 0
	}
	if v, ok2 := venues[venue+":"+venueType]; ok2 && v.Bid > 0 {
		return v.Bid
	}
	for _, v := range venues {
		if v.Mid() > 0 {
			return v.Mid()
		}
	}
	return 0
}
