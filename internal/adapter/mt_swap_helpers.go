package adapter

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

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

// swapZeroAlertInterval 整轮零产出告警节流（每 venue 内存节流 6h）。
const swapZeroAlertInterval = 6 * time.Hour

// swapZeroOutputAlert 整轮零产出（emit+emit0==0 且 skip/err>0）→ critical 告警
// （AlertSink 已注入链 main→brokerMgr→ad；nil=no-op）。PXBT 双账户静默零 facts
// 实证（2026-09-30）：skip=81 但只有日志行无人看——禁静默升级为告警。
func (a *MT5Adapter) swapZeroOutputAlert(emit, emit0 int, skips, errs []string) {
	if emit+emit0 > 0 || len(skips)+len(errs) == 0 || a.AlertSink == nil {
		return
	}
	a.mu.Lock()
	if time.Since(a.swapZeroAlertAt) < swapZeroAlertInterval {
		a.mu.Unlock()
		return
	}
	a.swapZeroAlertAt = time.Now()
	a.mu.Unlock()
	reasons := append(append([]string{}, skips...), errs...)
	if len(reasons) > 5 {
		reasons = append(reasons[:5], fmt.Sprintf("…+%d", len(reasons)-5))
	}
	a.AlertSink("critical", fmt.Sprintf("[mt5:%s] swap 采集整轮零产出：skip=%d err=%d（%s）",
		a.venue, len(skips), len(errs), strings.Join(reasons, "; ")))
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
// D-104 P1 T2：加 canon + price 参——canonical 以 "JPY" 结尾时 per-lot 值除以 price
// （JPY 计价货币额 → USD 换算，防 JPY 对 per-lot 值偏大误报）。
func swapInPointsGuardRail(src, venue, canon string, s, point, cs, price float64) bool {
	if cs <= 0 || point <= 0 {
		return false
	}
	perLot := math.Abs(s) * point * cs
	if strings.HasSuffix(canon, "JPY") && price > 0 {
		perLot /= price // JPY 计价 → USD 换算
	}
	if perLot > swapInPointsGuardUSD {
		log.Printf("[%s:%s] [swap] WARN in-points per-lot USD=%.2f > %.0f (sym=%s s=%.4f point=%.6f cs=%.0f) — emit retained, possible semantic drift",
			src, venue, perLot, swapInPointsGuardUSD, canon, s, point, cs)
		return true
	}
	return false
}

// weekdayNames Go time.Weekday → 中文名（日志可读，D-077 V2）。
var weekdayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// tripleSwapDayOverrides D-115：实测 override——MT5 API V3DaysSwap 字段不可信时，
// 用实测值覆盖（规格≠实际结算，经验库 mt5-spec-vs-settlement-disconnect 同类）。
// key=venue，value=Go time.Weekday（0=Sunday...6=Saturday）。
// 新增 venue 需有 position_swap_snapshots 实证（活仓结算跳变倍数对拍）。
var tripleSwapDayOverrides = map[string]float64{
	"REDACTED_MT_ACCT": 3, // Wednesday，8-26 实证：#290 0.05→0.19≈3x
	"39050818":         3, // Wednesday，8-26 实证：#291 0.04→0.16=3x
}

// applyTripleSwapDayOverride D-115：venue 在 override map 里 → 全符号替换为 override 值。
// 不在 map 里 → 原样返回（fail-closed，等活仓实证再改）。
func applyTripleSwapDayOverride(venue string, tripleDays map[string]float64) map[string]float64 {
	override, ok := tripleSwapDayOverrides[venue]
	if !ok {
		return tripleDays
	}
	out := make(map[string]float64, len(tripleDays))
	for canon := range tripleDays {
		out[canon] = override
	}
	return out
}

// collectTripleSwapDayFacts 三倍日 per-symbol 全量收集（D-077 修复⑪A M3）。
// 同 venue 值一致 → 发 venue 级 Fact（symbol=venue）；不一致 → 发 per-symbol Fact + warn。
// 空 map → 不发（全失败，下游 fail-closed 跳过）。
// D-115：入口应用 tripleSwapDayOverrides（实测值覆盖 API 不可信字段）。
func collectTripleSwapDayFacts(src, venue string, tripleDays map[string]float64) []fact.Fact {
	tripleDays = applyTripleSwapDayOverride(venue, tripleDays)
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
		// D-496 R4：账号级 fact symbol=NULL（存侧 ''→NULL 归位），不再挪用 symbol=venue。
		return []fact.Fact{tripleSwapDayFact(venue, "", first, src)}
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

// marginFact 构造保证金率 Fact（S6 返修）：账户级，value=1/杠杆（无限杠杆=0），
// Symbol=venue（账户标识，非符号）。边界：符号级费率网关不支持，本包不承诺
// （风控消费如需符号级另行立项，先例=S6 原设计「多档语义留待扩展」同型）。
func marginFact(kind, venue, symbol string, rate float64, src string) fact.Fact {
	return fact.Fact{
		Kind:   kind,
		Venue:  venue,
		Symbol: symbol,
		Value:  rate,
		Unit:   fact.UnitRatio,
		Ts:     time.Now(),
		Src:    src,
	}
}

// fetchLeverage 取账户级杠杆（S6 返修，对照 swapPerCallTimeout 先例）。
// 成功返回 (leverage, true)；失败 / result nil / Leverage<=0 → 打日志返回 (0, false)
// （Leverage<=0 守卫防 1/0=+Inf 写入 facts 表；日志在 helpers 内打，mt_swap.go 无 log import）。
func (a *MT5Adapter) fetchLeverage(ctx context.Context) (float64, bool) {
	pctx, cancel := context.WithTimeout(a.withSessionMD(ctx), swapPerCallTimeout)
	defer cancel()
	resp, err := a.mt5.AccountSummary(pctx, &mt5pb.AccountSummaryRequest{Id: a.getToken()})
	if err != nil {
		log.Printf("margin: leverage fetch failed: %v", err)
		return 0, false
	}
	if e := resp.GetError(); e != nil {
		log.Printf("margin: leverage fetch failed: %s", e.GetMessage())
		return 0, false
	}
	r := resp.GetResult()
	if r == nil {
		log.Printf("margin: leverage fetch failed: result nil")
		return 0, false
	}
	lev := r.GetLeverage()
	if lev <= 0 {
		log.Printf("margin: leverage fetch failed: non-positive leverage %v", lev)
		return 0, false
	}
	return lev, true
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
