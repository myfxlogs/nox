package adapter

// MT swap 归一化（§3.6，D-036）。
// 语义沿用归档系统生产实证（/opt/arb-archive/internal/evaluator/swap.go，D-058；归档未随迁已佚——fossil-allow 出处引证）：
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
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
	"arb/internal/safego"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

// ErrSwapColdStart 启动后报价尚未流入：venue 有需价格归一的 symbol 但无任何报价。
// BrokerManager 聚合到后整轮退避重试（§3.6 冷启动补全），直到报价齐或超时接受部分。
var ErrSwapColdStart = errors.New("swap cold start: no venue quote")

// SwapMode/swapFreeServer/normalizeSwapBpsDaily/swapNeedsPrice/mt5SwapMode 族已拆至
// mt_swap_mode.go（D-309 纯移动）。

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
// D-104 P1 T3：有界并发 8（semaphore + per-index 结果槽，循环后合并）。
func (a *MT5Adapter) SwapFacts(ctx context.Context, q *bus.QuoteBus) ([]fact.Fact, error) {
	sm := a.SymbolMap()
	if len(sm) == 0 {
		return nil, nil
	}
	snap := q.Snapshot(ctx, symbolSet(sm))
	// D-383 A2.3：canonical 标签发射闸——多行组非 primary 行仅 canonical 标签停发，
	// native/variant fact 照发（D-325 语义保留）。blocked 集快照单次读取（同 SymbolMap 并发风格）。
	blocked := a.emitBlockedSet()

	// D-104 P1 T3：有界并发 8——符号列表 + per-index 结果槽（含全部产出），循环后按序合并。
	type symEntry struct{ brokerSym, canon string }
	syms := make([]symEntry, 0, len(sm))
	for brokerSym, canon := range sm {
		syms = append(syms, symEntry{brokerSym, canon})
	}
	type swapSlot struct {
		facts        []fact.Fact
		errs         []string
		skips        []string
		emit, emit0  int
		missingPrice int
		missingSym   string
		uncoveredRaw int32
		hasUncovered bool
		tripleDay    float64
		hasTripleDay bool
	}
	slots := make([]swapSlot, len(syms))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	// D-104 P1 R1：循环前捕获 token，goroutine 内用同一 token（消除 8 路并发无锁读 a.token，
	// 与 reconnectSession 持 a.mu 写的竞态面；getToken 持锁，同一轮采集语义不变）。
	tok := a.getToken()
	for i, ent := range syms {
		wg.Add(1)
		sem <- struct{}{}
		i, ent := i, ent
		safego.Go("mt_swap:symbol-params", func() {
			defer wg.Done()
			defer func() { <-sem }()
			slot := &slots[i]
			brokerSym, canon := ent.brokerSym, ent.canon
			// S5：派生 per-call 超时，慢符号进 errs 不阻塞整体（parent ctx 不变）。
			pctx, cancel := context.WithTimeout(a.withSessionMD(ctx), swapPerCallTimeout)
			resp, err := a.mt5.SymbolParams(pctx, &mt5pb.SymbolParamsRequest{Id: tok, Symbol: brokerSym})
			cancel()
			if err != nil {
				slot.errs = append(slot.errs, brokerSym+": "+err.Error())
				return
			}
			if e := resp.GetError(); e != nil {
				slot.errs = append(slot.errs, brokerSym+": "+e.GetMessage())
				return
			}
			r := resp.GetResult()
			if r == nil {
				slot.skips = append(slot.skips, brokerSym+": result nil")
				return
			}
			sg := r.GetSymbolGroup()
			si := r.GetSymbolInfo()
			if sg == nil {
				slot.skips = append(slot.skips, brokerSym+": symbolGroup nil")
				return
			}
			// D-077 修复⑪A M3：逐符号收集 ThreeDaysSwap。
			slot.tripleDay = mt5TripleSwapDay(sg.GetThreeDaysSwap())
			slot.hasTripleDay = true
			// trade_mode 不依赖 swap 标定（D-060 F3）：emit/emit0/标定 skip 的符号都发。
			// D-383 A2.3：blocked 行仅 canonical 标签停发（native 照发）。
			if !blocked[brokerSym] {
				slot.facts = append(slot.facts, tradeModeFact(a.venue, canon, sg.GetTradeMode(), "mt5"))
			}
			// D-325 S1：native variant 双发 trade_mode——与 swap 双发同据（brokerSym==canon 跳过防重复行）。
			if brokerSym != canon {
				slot.facts = append(slot.facts, tradeModeFact(a.venue, brokerSym, sg.GetTradeMode(), "mt5"))
			}
			mode := mt5SwapModeForVenue(a.venue, sg.GetSwapType(), sg.GetSwapLong(), sg.GetSwapShort())
			// D-325 S1：先捕获 raw 模式——下方 Exness override 只作用于 canonical（D-300 S0 裁定），
			// native variant fact 恒用 raw 值（XAUUSDm 周末腿判据需分辨 XAUUSDm/XAUUSD247m 真实差异）。
			rawMode := mode
			// D-300 S0 定案（业主 2026-09-16 两轮断言+规格直连亲证）→D-342R1 修订①（收窄为
			// (server,broker_symbol) 白名单，mt_swap_mode.go swapFreeWhitelist）：Exness swap-free
			// 营销——规格字段系名义标价、账单不执行（real 账户 XAUUSDm SwapLong=-534.9 但实收 0）。
			// 白名单命中 symbol swap 置 0 发射（走 SwapModeNone 零值路径——零息 venue 是 carry
			// 最佳对手的 facts 语义保留）；同时消解 m 后缀品种折叠对标准 canonical 的名义值污染
			// （Exness-MT5Real 3001157 XAUUSD canonical 曾被 XAUUSDm 名义 swap 污染为 -1.2481 bps/day）。
			// D-342R1：Trial5(277842155) 账单实证执行（492 XAUUSD247m 实收 -0.55/日）→ 仅标准形免，
			// variant 走正常值路径（raw bps/day 入 canonical→swapledger 入账）。
			if swapFreeServer(a.server, brokerSym) && mode != SwapModeNone {
				mode = SwapModeNone // 下游 switch 自动按 emit0 计数（零值路径），此处只覆盖 mode
			}
			if mode == SwapModeUncalibrated {
				slot.uncoveredRaw = int32(sg.GetSwapType())
				slot.hasUncovered = true
				slot.skips = append(slot.skips, mt5SkipReason(brokerSym, sg))
				return
			}
			point, cs := 0.0, 0.0
			if si != nil {
				point, cs = si.GetPoints(), si.GetContractSize()
			}
			price := venuePrice(snap, canon, a.venue, "mt5")
			if swapNeedsPrice(mode) && price <= 0 {
				slot.missingPrice = 1
				slot.missingSym = brokerSym
				return
			}
			n := 0
			if !blocked[brokerSym] {
				if l, ok := normalizeSwapBpsDaily(mode, sg.GetSwapLong(), price, point, cs); ok {
					slot.facts = append(slot.facts, swapFact(fact.KindSwapLong, a.venue, canon, l, "mt5"))
					n++
				}
				if s, ok := normalizeSwapBpsDaily(mode, sg.GetSwapShort(), price, point, cs); ok {
					slot.facts = append(slot.facts, swapFact(fact.KindSwapShort, a.venue, canon, s, "mt5"))
					n++
				}
			} else {
				slot.skips = append(slot.skips, brokerSym+": canonical emit suppressed (non-primary group variant, D-383)")
			}
			switch {
			case blocked[brokerSym]:
				// canonical 停发行不进 emit 计数（native 仍在下方照发）
			case n == 0:
				slot.skips = append(slot.skips, brokerSym+": normalize failed (no contract size?)")
			case mode == SwapModeNone:
				slot.emit0++ // 真停用发零值对（零息 venue 是 carry 最佳对手）
			default:
				slot.emit++
			}
			// D-325 S1：native variant swap 双发——rawMode+raw 值（不经 D-300 S0 override），
			// 同一 SymbolParams 响应零新增 RPC；缺价时 normalize 自返回 ok=false（该轮零发射，
			// 与 canonical fail-safe 一致）；rawMode==Uncalibrated 已在上方提前返回不会到达。
			if brokerSym != canon {
				if l, ok := normalizeSwapBpsDaily(rawMode, sg.GetSwapLong(), price, point, cs); ok {
					slot.facts = append(slot.facts, swapFact(fact.KindSwapLong, a.venue, brokerSym, l, "mt5"))
				}
				if s, ok := normalizeSwapBpsDaily(rawMode, sg.GetSwapShort(), price, point, cs); ok {
					slot.facts = append(slot.facts, swapFact(fact.KindSwapShort, a.venue, brokerSym, s, "mt5"))
				}
			}
			// D-092 护栏：InPoints emit 后检查每手日 swap 美元额（防 FxPro mode 9 语义漂移）。
			// D-104 P1 T2：传 canon + price（JPY 计价 → USD 换算）。
			if mode == SwapModeInPoints && !blocked[brokerSym] {
				swapInPointsGuardRail("mt5", a.venue, canon, sg.GetSwapLong(), point, cs, price)
				swapInPointsGuardRail("mt5", a.venue, canon, sg.GetSwapShort(), point, cs, price)
			}
		})
	}
	wg.Wait()

	// D-104 P1 T3：按序合并 per-index 结果槽。
	var facts []fact.Fact
	var errs []string
	var skips []string
	emit, emit0 := 0, 0
	missingPrice := 0
	missingSyms := make([]string, 0)
	uncovered := map[int32]int{}
	tripleDays := map[string]float64{}
	for i, ent := range syms {
		slot := &slots[i]
		facts = append(facts, slot.facts...)
		errs = append(errs, slot.errs...)
		skips = append(skips, slot.skips...)
		emit += slot.emit
		emit0 += slot.emit0
		missingPrice += slot.missingPrice
		if slot.missingSym != "" {
			missingSyms = append(missingSyms, slot.missingSym)
		}
		if slot.hasUncovered {
			uncovered[slot.uncoveredRaw]++
		}
		if slot.hasTripleDay {
			// D-383 A2.3：canonical 键只在合格行收集（blocked 行仅 native 键）。
			if !blocked[ent.brokerSym] {
				tripleDays[ent.canon] = slot.tripleDay
			}
			// D-325 S1：native variant 并列入列（key 不同不冲突），collectTripleSwapDayFacts 按 key 发射。
			if ent.brokerSym != ent.canon {
				tripleDays[ent.brokerSym] = slot.tripleDay
			}
		}
	}
	swapPollSummary("mt5", a.venue, emit, emit0, skips)
	swapUncoveredSummary("mt5", a.venue, uncovered)
	// D-077 修复⑪A M3：三倍日 per-symbol 全量收集（删「首个符号代表 venue」）。
	// 同 venue 值一致 → 发 venue 级 Fact（现行为）；不一致 → 发 per-symbol Fact + warn。
	facts = append(facts, collectTripleSwapDayFacts("mt5", a.venue, tripleDays)...)
	if missingPrice > 0 {
		// D-104 P0 返修③ R-c1：facts 与 ErrSwapColdStart 同返（保留 60s bound 整轮重试信号），
		// 不再丢弃整 venue facts——冷符号等下一轮补，其余 facts 立即落地。
		// R-c2：缺报价符号名单进错误消息（前 10 + 超出计 "…+N"），下轮日志可定位。
		return facts, fmt.Errorf("%w: %d symbol(s) lack venue quote: %s", ErrSwapColdStart, missingPrice, summarizeMissingSyms(missingSyms))
	}
	return swapResult(a.venue, facts, errs)
}
