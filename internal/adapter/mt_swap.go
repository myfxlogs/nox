package adapter

// MT swap 归一化（§3.6，D-036）。
// 语义沿用归档系统生产实证（/opt/arb-archive/internal/evaluator/swap.go，D-058）：
//   - 百分比模式为年化% → 每日 bps 与价格无关（S/100/365×10⁴）；
//   - mode 0 真停用发 0 值 Fact（零息 venue 是 carry 最佳对手，必须可配对）；
//   - mode 0 却带非零 rates = 矛盾 broker 数据（PXB 事故实证）→ 跳过，宁丢机会不交易不可信经济性；
//   - mode 2/7/8/超界未标定 → 跳过。
// 产出的 Fact：kind=swap_long/swap_short，unit=bps_daily（正=收，负=付），src=mt5/mt4。

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
	SwapModeNone             SwapMode = iota // 真停用：发 0 值 Fact
	SwapModeInPoints                         // 点数：S × Points / price
	SwapModePerLotCurrency                   // 每手货币额：S / (price × ContractSize)
	SwapModeAnnualPct                        // 年化百分比：S% / 365
	SwapModeUncalibrated                     // 未标定 / 矛盾数据：跳过
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
func mt5SwapMode(t mt5pb.SwapType, long, short float64) SwapMode {
	switch t {
	case mt5pb.SwapType_SwapType_SwapNone:
		if long != 0 || short != 0 {
			return SwapModeUncalibrated // 矛盾数据
		}
		return SwapModeNone
	case mt5pb.SwapType_SwapType_InPoints:
		return SwapModeInPoints
	case mt5pb.SwapType_SwapType_MarginCurrency, mt5pb.SwapType_SwapType_Currency:
		return SwapModePerLotCurrency
	case mt5pb.SwapType_SwapType_PercCurPrice, mt5pb.SwapType_SwapType_PercOpenPrice:
		return SwapModeAnnualPct
	default: // SymInfo_s408(2) / PointClosePrice(7) / PointBidPrice(8) / 超界
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

// SwapFacts 轮询全部已映射符号的 SymbolParams，归一为 swap_long/swap_short bps_daily Fact（§3.6）。
// price 取本 venue 最新报价（缺失回退任一 venue 中价，bps 精度下价格差异可忽略）。
// 单符号失败只记日志；全失败才返回错误（Scheduler 退避重试）。
func (a *MT5Adapter) SwapFacts(ctx context.Context, q *bus.QuoteBus) ([]fact.Fact, error) {
	sm := a.SymbolMap()
	if len(sm) == 0 {
		return nil, nil
	}
	snap := q.Snapshot(ctx, symbolSet(sm))
	var facts []fact.Fact
	var errs []string
	missingPrice := 0
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
			continue
		}
		sg := r.GetSymbolGroup()
		si := r.GetSymbolInfo()
		if sg == nil {
			continue
		}
		mode := mt5SwapMode(sg.GetSwapType(), sg.GetSwapLong(), sg.GetSwapShort())
		if mode == SwapModeUncalibrated {
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
		if l, ok := normalizeSwapBpsDaily(mode, sg.GetSwapLong(), price, point, cs); ok {
			facts = append(facts, swapFact(fact.KindSwapLong, a.venue, canon, l, "mt5"))
		}
		if s, ok := normalizeSwapBpsDaily(mode, sg.GetSwapShort(), price, point, cs); ok {
			facts = append(facts, swapFact(fact.KindSwapShort, a.venue, canon, s, "mt5"))
		}
	}
	if missingPrice > 0 {
		return nil, fmt.Errorf("%w: %d symbol(s) lack venue quote", ErrSwapColdStart, missingPrice)
	}
	return swapResult(a.venue, facts, errs)
}

// SwapFacts MT4 版。swap 语义字段在 SymbolInfo.Ex（SymbolInfoEx）。
func (a *MT4Adapter) SwapFacts(ctx context.Context, q *bus.QuoteBus) ([]fact.Fact, error) {
	sm := a.SymbolMap()
	if len(sm) == 0 {
		return nil, nil
	}
	snap := q.Snapshot(ctx, symbolSet(sm))
	var facts []fact.Fact
	var errs []string
	missingPrice := 0
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
			continue
		}
		si := r.GetSymbol()
		if si == nil || si.GetEx() == nil {
			errs = append(errs, brokerSym+": no SymbolInfo.Ex (swap 字段缺失)")
			continue
		}
		ex := si.GetEx()
		mode := mt4SwapMode(ex.GetSwapType(), ex.GetSwapEnable(), ex.GetSwapLong(), ex.GetSwapShort())
		if mode == SwapModeUncalibrated {
			continue
		}
		point, cs := ex.GetPoint(), ex.GetContractSize()
		price := venuePrice(snap, canon, a.venue, "mt4")
		if swapNeedsPrice(mode) && price <= 0 {
			missingPrice++
			continue
		}
		if l, ok := normalizeSwapBpsDaily(mode, ex.GetSwapLong(), price, point, cs); ok {
			facts = append(facts, swapFact(fact.KindSwapLong, a.venue, canon, l, "mt4"))
		}
		if s, ok := normalizeSwapBpsDaily(mode, ex.GetSwapShort(), price, point, cs); ok {
			facts = append(facts, swapFact(fact.KindSwapShort, a.venue, canon, s, "mt4"))
		}
	}
	if missingPrice > 0 {
		return nil, fmt.Errorf("%w: %d symbol(s) lack venue quote", ErrSwapColdStart, missingPrice)
	}
	return swapResult(a.venue, facts, errs)
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
