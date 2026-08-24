package adapter

// MT swap 归一化（§3.6，D-036）。
// 语义沿用归档系统生产实证（/opt/arb-archive/internal/evaluator/swap.go，D-058）：
//   - 百分比模式为年化% → 每日 bps 与价格无关（S/100/365×10⁴）；
//   - mode 0 真停用发 0 值 Fact（零息 venue 是 carry 最佳对手，必须可配对）；
//   - mode 0 却带非零 rates = 矛盾 broker 数据（PXB 事故实证）→ 跳过，宁丢机会不交易不可信经济性；
//   - mode 2 SymInfo_s408 = 标的货币额（WP1 补齐，同 3/4 口径 S/(price×CS)×1e4）；
//   - mode 7/8 未标定 → 跳过 + 计数日志（WP1 禁静默）；mode 9=FxPro 扩展 InPoints（D-092 标定）。
// 产出的 Fact：kind=swap_long/swap_short，unit=bps_daily（正=收，负=付），src=mt5/mt4；
// 以及 kind=trade_mode（unit=mode，MT5 ENUM 透传，D-060 F3——与 swap 标定无关，跳过符号也发）。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"

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
	SwapModeUncalibrated                   // 未标定 / 矛盾数据：跳过 + 计数日志（mode 7/8/10+）
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
//	9 FxPro 扩展       → SwapModeInPoints（D-092 标定：同 broker 全部其他符号=InPoints 且
//	                    point/CS 同构；点数解读 ±$5.5~94/手/日 包住 Exness 同符号区间；
//	                    证据见 docs/油类SwapType9标定-施工提示词.md S1 表，2026-08-24 采集）
//	10+ 超界           → SwapModeUncalibrated（proto 未定义；计数日志禁静默）
func mt5SwapMode(t mt5pb.SwapType, long, short float64) SwapMode {
	switch t {
	case mt5pb.SwapType_SwapType_SwapNone:
		if long != 0 || short != 0 {
			return SwapModeUncalibrated // 矛盾数据
		}
		return SwapModeNone
	case mt5pb.SwapType_SwapType_InPoints, mt5pb.SwapType(9): // 9=FxPro 扩展 InPoints（D-092）
		return SwapModeInPoints
	case mt5pb.SwapType_SwapType_SymInfo_s408, mt5pb.SwapType_SwapType_MarginCurrency, mt5pb.SwapType_SwapType_Currency:
		return SwapModePerLotCurrency
	case mt5pb.SwapType_SwapType_PercCurPrice, mt5pb.SwapType_SwapType_PercOpenPrice:
		return SwapModeAnnualPct
	default: // PointClosePrice(7) / PointBidPrice(8) / 超界(10+)
		return SwapModeUncalibrated
	}
}

// swapPerCallTimeout 单符号 SymbolParams 调用超时（S5，2026-08-24）。
// PXBT 实证：261 次 DeadlineExceeded 全部因单 venue 慢 SymbolParams 拖垮整轮 poll
// （共享 parent ctx 不自带超时 → 全符号超时 facts 空 → swapResult 返回 err）。
// per-call 3s × 符号数 ≤ 35s 的 venue 完全不受影响；符号数多时部分符号进 errs/skips，
// facts>0 仍部分成功（swapResult 语义：errs 只打日志），优于全挂。
const swapPerCallTimeout = 3 * time.Second

// SwapFacts 轮询全部已映射符号的 SymbolParams，归一为 swap_long/swap_short bps_daily Fact（§3.6），
// 并对每个有 SymbolGroup 的符号顺带产出 trade_mode Fact（D-060 F3，零额外 RPC）。
// price 取本 venue 最新报价（缺失回退任一 venue 中价，bps 精度下价格差异可忽略）。
// 单符号失败只记日志；全失败才返回错误（Scheduler 退避重试）。
// 每轮每 venue 一行汇总日志（D-060 F1 跳过可观测）：emit/emit0 按符号计，skip 附原因。
// WP1：未覆盖 SwapType 计数（7/8/9+）单独汇总日志，禁静默（探测面隐形收缩 P4）。
// S5：每符号 SymbolParams 派生 per-call 3s 超时（慢 venue 不拖垮整轮，facts>0 仍部分成功）。
func (a *MT5Adapter) SwapFacts(ctx context.Context, q *bus.QuoteBus) ([]fact.Fact, error) {
	sm := a.SymbolMap()
	if len(sm) == 0 {
		return nil, nil
	}
	// S6 返修：账户级杠杆 → init_margin_rate（网关不返回符号级 margin 字段，实证见返修提示词③）。
	// fetchLeverage 失败只打日志（helpers 内）不进 errs——errs 会误伤 swapResult 全失败判定；
	// 本轮无 margin fact，下轮再试。Leverage<=0 守卫在 fetchLeverage 内（防 1/0=Inf）。
	var marginRate float64
	hasMarginRate := false
	if lev, ok := a.fetchLeverage(ctx); ok {
		hasMarginRate = true
		marginRate = 1 / lev
		if lev >= 1e6 { // Exness 无限杠杆哨兵实证（2e9）：零保证金账号发 0 值（SwapModeNone 先例）
			marginRate = 0
		}
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
		// S5：派生 per-call 超时，慢符号进 errs 不阻塞整体（parent ctx 不变）。
		pctx, cancel := context.WithTimeout(a.withSessionMD(ctx), swapPerCallTimeout)
		resp, err := a.mt5.SymbolParams(pctx, &mt5pb.SymbolParamsRequest{Id: a.token, Symbol: brokerSym})
		cancel()
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
		// D-092 护栏：InPoints emit 后检查每手日 swap 美元额（防 FxPro mode 9 语义漂移）。
		if mode == SwapModeInPoints {
			swapInPointsGuardRail("mt5", a.venue, brokerSym, sg.GetSwapLong(), point, cs)
			swapInPointsGuardRail("mt5", a.venue, brokerSym, sg.GetSwapShort(), point, cs)
		}
	}
	swapPollSummary("mt5", a.venue, emit, emit0, skips)
	swapUncoveredSummary("mt5", a.venue, uncovered)
	// D-077 修复⑪A M3：三倍日 per-symbol 全量收集（删「首个符号代表 venue」）。
	// 同 venue 值一致 → 发 venue 级 Fact（现行为）；不一致 → 发 per-symbol Fact + warn。
	facts = append(facts, collectTripleSwapDayFacts("mt5", a.venue, tripleDays)...)
	// S6 返修：账户级 init_margin_rate（Symbol=venue，账户标识非符号）。
	if hasMarginRate {
		facts = append(facts, marginFact(fact.KindInitMarginRate, a.venue, a.venue, marginRate, "mt5"))
	}
	if missingPrice > 0 {
		return nil, fmt.Errorf("%w: %d symbol(s) lack venue quote", ErrSwapColdStart, missingPrice)
	}
	return swapResult(a.venue, facts, errs)
}
