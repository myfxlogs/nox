package adapter

// MT swap 归一化（§3.6，D-036）。
// 语义沿用归档系统生产实证（/opt/arb-archive/internal/evaluator/swap.go，D-058）：
//   - 百分比模式为年化% → 每日 bps 与价格无关（S/100/365×10⁴）；
//   - mode 0 真停用发 0 值 Fact（零息 venue 是 carry 最佳对手，必须可配对）；
//   - mode 0 却带非零 rates = 矛盾 broker 数据（PXB 事故实证）→ 跳过，宁丢机会不交易不可信经济性；
//   - mode 2 SymInfo_s408 = 标的货币额（WP1 补齐，同 3/4 口径 S/(price×CS)×1e4）；
//   - mode 7/8/超界(9+) 未标定 → 跳过 + 计数日志（WP1 禁静默）。
// 产出的 Fact：kind=swap_long/swap_short，unit=bps_daily（正=收，负=付），src=mt5/mt4；
// 以及 kind=trade_mode（unit=mode，MT5 ENUM 透传，D-060 F3——与 swap 标定无关，跳过符号也发）。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"

	mt4pb "arb/internal/adapter/gen/mtapi/mt4"
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

// ErrSwapColdStart 启动后报价尚未流入：venue 有需价格归一的 symbol 但无任何报价。
// BrokerManager 聚合到后整轮退避重试（§3.6 冷启动补全），直到报价齐或超时接受部分。
var ErrSwapColdStart = errors.New("swap cold start: no venue quote")

// SwapMode 语义化 swap 模式（MT4/MT5 原始值映射到此枚举后统一换算）。
type SwapMode int

const (
	SwapModeNone           SwapMode = iota // 真停用：发 0 值 Fact
	SwapModeInPoints                       // 点数：S × Points / price
	SwapModePerLotCurrency                 // 每手货币额：S / (price × ContractSize)（mode 2/3/4）
	SwapModeAnnualPct                      // 年化百分比：S% / 365（mode 5/6）
	SwapModeUncalibrated                   // 未标定 / 矛盾数据：跳过 + 计数日志（mode 7/8/9+）
)

// normalizeSwapBpsDaily 归一单方向 swap 原始值为日 bps（正=收，负=付）。
// ok=false 表示缺价格或未标定，调用方跳过该 Fact。
func normalizeSwapBpsDaily(mode SwapMode, s, price, point, contractSize float64) (float64, bool) {
	switch mode {
	case SwapModeNone:
		return 0, true
	case SwapModeInPoints:
		if price <= 0 {
			return 0, false
		}
		return s * point / price * 10000, true
	case SwapModePerLotCurrency:
		if price <= 0 || contractSize <= 0 {
			return 0, false
		}
		return s / (price * contractSize) * 10000, true
	case SwapModeAnnualPct:
		// S% 年化 / 365 天 → 日百分比 → bps
		return s / 100 / 365 * 10000, true
	default:
		return 0, false
	}
}

// swapNeedsPrice 该 mode 的归一化是否依赖报价（None/AnnualPct 与价格无关，冷启动仍可发 Fact）。
func swapNeedsPrice(mode SwapMode) bool {
	return mode == SwapModeInPoints || mode == SwapModePerLotCurrency
}

// mt5SwapMode 映射 MT5 ENUM_SYMBOL_SWAP_MODE（proto 透传原始值，D-058 实证数值对齐）。
// 完整映射表（proto 枚举 0~8 + FxPro 实证超界值 9）：
//
//	0 SwapNone         → SwapModeNone（真停用发 0 值 Fact；非零 rates=矛盾→Uncalibrated）
//	1 InPoints         → SwapModeInPoints（S × Points / price × 1e4）
//	2 SymInfo_s408     → SwapModePerLotCurrency（标的货币额，S / (price × CS) × 1e4；同 3/4 口径）
//	3 MarginCurrency   → SwapModePerLotCurrency（保证金货币额，同上）
//	4 Currency         → SwapModePerLotCurrency（入金/利润货币额，同上）
//	5 PercCurPrice     → SwapModeAnnualPct（S% / 365 × 1e2）
//	6 PercOpenPrice    → SwapModeAnnualPct（同 5，用开仓价≈现价）
//	7 PointClosePrice  → SwapModeUncalibrated（重开仓模式，点数±收盘价，非线性无法归一）
//	8 PointBidPrice    → SwapModeUncalibrated（重开仓模式，点数±买价，同上）
//	9+ 超界            → SwapModeUncalibrated（FxPro WTI 实证=9，proto 未定义；计数日志禁静默）
func mt5SwapMode(t mt5pb.SwapType, long, short float64) SwapMode {
	switch t {
	case mt5pb.SwapType_SwapType_SwapNone:
		if long != 0 || short != 0 {
			return SwapModeUncalibrated // 矛盾数据
		}
		return SwapModeNone
	case mt5pb.SwapType_SwapType_InPoints:
		return SwapModeInPoints
	case mt5pb.SwapType_SwapType_SymInfo_s408, mt5pb.SwapType_SwapType_MarginCurrency, mt5pb.SwapType_SwapType_Currency:
		return SwapModePerLotCurrency
	case mt5pb.SwapType_SwapType_PercCurPrice, mt5pb.SwapType_SwapType_PercOpenPrice:
		return SwapModeAnnualPct
	default: // PointClosePrice(7) / PointBidPrice(8) / 超界(9+)
		return SwapModeUncalibrated
	}
}

// mt4SwapMode 映射 MT4 服务端 swap_type（0=点数 1=基准货币/手 2=年化% 3=保证金货币/手）。
// MT4 无现网实证，语义按 MT4 服务端惯例，上线后以真实数据核验。
func mt4SwapMode(t, swapEnable int32, long, short float64) SwapMode {
	if swapEnable == 0 {
		if long != 0 || short != 0 {
			return SwapModeUncalibrated
		}
		return SwapModeNone
	}
	switch t {
	case 0:
		return SwapModeInPoints
	case 1, 3:
		return SwapModePerLotCurrency
	case 2:
		return SwapModeAnnualPct
	default:
		return SwapModeUncalibrated
	}
}

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

// SwapFacts 轮询全部已映射符号的 SymbolParams，归一为 swap_long/swap_short bps_daily Fact（§3.6），
// 并对每个有 SymbolGroup 的符号顺带产出 trade_mode Fact（D-060 F3，零额外 RPC）。
// price 取本 venue 最新报价（缺失回退任一 venue 中价，bps 精度下价格差异可忽略）。
// 单符号失败只记日志；全失败才返回错误（Scheduler 退避重试）。
// 每轮每 venue 一行汇总日志（D-060 F1 跳过可观测）：emit/emit0 按符号计，skip 附原因。
// WP1：未覆盖 SwapType 计数（7/8/9+）单独汇总日志，禁静默（探测面隐形收缩 P4）。
func (a *MT5Adapter) SwapFacts(ctx context.Context, q *bus.QuoteBus) ([]fact.Fact, error) {
	sm := a.SymbolMap()
	if len(sm) == 0 {
		return nil, nil
	}
	snap := q.Snapshot(ctx, symbolSet(sm))
	var facts []fact.Fact
	var errs []string
	var skips []string
	emit, emit0 := 0, 0
	missingPrice := 0
	uncovered := map[int32]int{}       // SwapType 原始值 → 命中符号数（WP1 计数日志）
	tripleDays := map[string]float64{} // D-077 修复⑪A M3：canon → 三倍日（per-symbol 全量收集）
	for brokerSym, canon := range sm {
		resp, err := a.mt5.SymbolParams(a.withSessionMD(ctx), &mt5pb.SymbolParamsRequest{Id: a.token, Symbol: brokerSym})
		if err != nil {
			errs = append(errs, brokerSym+": "+err.Error())
			continue
		}
		if e := resp.GetError(); e != nil {
			errs = append(errs, brokerSym+": "+e.GetMessage())
			continue
		}
		r := resp.GetResult()
		if r == nil {
			skips = append(skips, brokerSym+": result nil")
			continue
		}
		sg := r.GetSymbolGroup()
		si := r.GetSymbolInfo()
		if sg == nil {
			skips = append(skips, brokerSym+": symbolGroup nil")
			continue
		}
		// D-077 修复⑪A M3：逐符号收集 ThreeDaysSwap（删「首个符号代表 venue」）。
		tripleDays[canon] = mt5TripleSwapDay(sg.GetThreeDaysSwap())
		// trade_mode 不依赖 swap 标定（D-060 F3）：emit/emit0/标定 skip 的符号都发。
		facts = append(facts, tradeModeFact(a.venue, canon, sg.GetTradeMode(), "mt5"))
		mode := mt5SwapMode(sg.GetSwapType(), sg.GetSwapLong(), sg.GetSwapShort())
		if mode == SwapModeUncalibrated {
			raw := int32(sg.GetSwapType())
			uncovered[raw]++
			skips = append(skips, mt5SkipReason(brokerSym, sg))
			continue
		}
		point, cs := 0.0, 0.0
		if si != nil {
			point, cs = si.GetPoints(), si.GetContractSize()
		}
		price := venuePrice(snap, canon, a.venue, "mt5")
		if swapNeedsPrice(mode) && price <= 0 {
			missingPrice++
			continue
		}
		n := 0
		if l, ok := normalizeSwapBpsDaily(mode, sg.GetSwapLong(), price, point, cs); ok {
			facts = append(facts, swapFact(fact.KindSwapLong, a.venue, canon, l, "mt5"))
			n++
		}
		if s, ok := normalizeSwapBpsDaily(mode, sg.GetSwapShort(), price, point, cs); ok {
			facts = append(facts, swapFact(fact.KindSwapShort, a.venue, canon, s, "mt5"))
			n++
		}
		switch {
		case n == 0:
			skips = append(skips, brokerSym+": normalize failed (no contract size?)")
		case mode == SwapModeNone:
			emit0++ // 真停用发零值对（零息 venue 是 carry 最佳对手）
		default:
			emit++
		}
	}
	swapPollSummary("mt5", a.venue, emit, emit0, skips)
	swapUncoveredSummary("mt5", a.venue, uncovered)
	// D-077 修复⑪A M3：三倍日 per-symbol 全量收集（删「首个符号代表 venue」）。
	// 同 venue 值一致 → 发 venue 级 Fact（现行为）；不一致 → 发 per-symbol Fact + warn。
	facts = append(facts, collectTripleSwapDayFacts("mt5", a.venue, tripleDays)...)
	if missingPrice > 0 {
		return nil, fmt.Errorf("%w: %d symbol(s) lack venue quote", ErrSwapColdStart, missingPrice)
	}
	return swapResult(a.venue, facts, errs)
}

// SwapFacts MT4 版。swap 语义字段在 SymbolInfo.Ex（SymbolInfoEx）。
// MT4 proto 无 TradeMode 字段 → 不产出 trade_mode Fact（下游未知放行，D-060 F3 边界）。
func (a *MT4Adapter) SwapFacts(ctx context.Context, q *bus.QuoteBus) ([]fact.Fact, error) {
	sm := a.SymbolMap()
	if len(sm) == 0 {
		return nil, nil
	}
	snap := q.Snapshot(ctx, symbolSet(sm))
	var facts []fact.Fact
	var errs []string
	var skips []string
	emit, emit0 := 0, 0
	missingPrice := 0
	uncovered := map[int32]int{}       // swap_type 原始值 → 命中符号数（WP1 计数日志）
	tripleDays := map[string]float64{} // D-077 修复⑪A M3：canon → 三倍日（per-symbol 全量收集）
	for brokerSym, canon := range sm {
		resp, err := a.mt4.SymbolParams(a.withSessionMD(ctx), &mt4pb.SymbolParamsRequest{Id: a.token, Symbol: brokerSym})
		if err != nil {
			errs = append(errs, brokerSym+": "+err.Error())
			continue
		}
		if e := resp.GetError(); e != nil {
			errs = append(errs, brokerSym+": "+e.GetMessage())
			continue
		}
		r := resp.GetResult()
		if r == nil {
			skips = append(skips, brokerSym+": result nil")
			continue
		}
		si := r.GetSymbol()
		if si == nil || si.GetEx() == nil {
			errs = append(errs, brokerSym+": no SymbolInfo.Ex (swap 字段缺失)")
			continue
		}
		ex := si.GetEx()
		// D-077 修复⑪A M3：逐符号收集 SwapRollover3Days（删「首个符号代表 venue」）。
		tripleDays[canon] = mt4TripleSwapDay(ex.GetSwapRollover3Days())
		mode := mt4SwapMode(ex.GetSwapType(), ex.GetSwapEnable(), ex.GetSwapLong(), ex.GetSwapShort())
		if mode == SwapModeUncalibrated {
			uncovered[ex.GetSwapType()]++
			if ex.GetSwapEnable() == 0 {
				skips = append(skips, brokerSym+": swap disabled with nonzero rates")
			} else {
				skips = append(skips, fmt.Sprintf("%s: uncalibrated mode %d", brokerSym, ex.GetSwapType()))
			}
			continue
		}
		point, cs := ex.GetPoint(), ex.GetContractSize()
		price := venuePrice(snap, canon, a.venue, "mt4")
		if swapNeedsPrice(mode) && price <= 0 {
			missingPrice++
			continue
		}
		n := 0
		if l, ok := normalizeSwapBpsDaily(mode, ex.GetSwapLong(), price, point, cs); ok {
			facts = append(facts, swapFact(fact.KindSwapLong, a.venue, canon, l, "mt4"))
			n++
		}
		if s, ok := normalizeSwapBpsDaily(mode, ex.GetSwapShort(), price, point, cs); ok {
			facts = append(facts, swapFact(fact.KindSwapShort, a.venue, canon, s, "mt4"))
			n++
		}
		switch {
		case n == 0:
			skips = append(skips, brokerSym+": normalize failed (no contract size?)")
		case mode == SwapModeNone:
			emit0++
		default:
			emit++
		}
	}
	swapPollSummary("mt4", a.venue, emit, emit0, skips)
	swapUncoveredSummary("mt4", a.venue, uncovered)
	// D-077 修复⑪A M3：三倍日 per-symbol 全量收集（删「首个符号代表 venue」）。
	facts = append(facts, collectTripleSwapDayFacts("mt4", a.venue, tripleDays)...)
	if missingPrice > 0 {
		return nil, fmt.Errorf("%w: %d symbol(s) lack venue quote", ErrSwapColdStart, missingPrice)
	}
	return swapResult(a.venue, facts, errs)
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
// MT4 proto: 0=Monday...4=Friday（无周末）；Go: 0=Sunday...6=Saturday。
// 映射：MT4 0(Mon)→1, 1(Tue)→2, 2(Wed)→3, 3(Thu)→4, 4(Fri)→5（+1 偏移）。
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
