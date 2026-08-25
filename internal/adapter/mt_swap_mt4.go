package adapter

// MT4 swap 归一化（§3.6，D-036）。MT4 专属映射 + SwapFacts 轮询。
// 共享类型/归一化/辅助函数在 mt_swap.go 与 mt_swap_helpers.go。

import (
	"context"
	"fmt"

	"arb/internal/bus"
	"arb/internal/fact"

	mt4pb "arb/internal/adapter/gen/mtapi/mt4"
)

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
	missingSyms := make([]string, 0)   // D-104 P0 返修③ R-c2：缺报价符号名单
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
			missingSyms = append(missingSyms, brokerSym)
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
		// D-092 护栏：InPoints emit 后检查每手日 swap 美元额（防 FxPro mode 9 语义漂移）。
		// D-104 P1 T2：传 canon + price（JPY 计价 → USD 换算）。
		if mode == SwapModeInPoints {
			swapInPointsGuardRail("mt4", a.venue, canon, ex.GetSwapLong(), point, cs, price)
			swapInPointsGuardRail("mt4", a.venue, canon, ex.GetSwapShort(), point, cs, price)
		}
	}
	swapPollSummary("mt4", a.venue, emit, emit0, skips)
	swapUncoveredSummary("mt4", a.venue, uncovered)
	// D-077 修复⑪A M3：三倍日 per-symbol 全量收集（删「首个符号代表 venue」）。
	facts = append(facts, collectTripleSwapDayFacts("mt4", a.venue, tripleDays)...)
	if missingPrice > 0 {
		// D-104 P0 返修③ R-c1/R-c2：facts 与 ErrSwapColdStart 同返 + 符号名单进错误消息（同 MT5）。
		return facts, fmt.Errorf("%w: %d symbol(s) lack venue quote: %s", ErrSwapColdStart, missingPrice, summarizeMissingSyms(missingSyms))
	}
	return swapResult(a.venue, facts, errs)
}
