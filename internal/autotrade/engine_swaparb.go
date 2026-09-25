package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/combo"
	"arb/internal/store/pgstore"
)

// swapArbLoop D-072 swap 套利：自动发现跨账户 swap 组合，名义额=broker 余额驱动。
// D-080 R7：每 loop 一个长生命周期 accumulator——当日累计、轮末 flush 覆盖写；
// 日切由 accumulator 内部 pending 队列处理（零定时器，无共享竞态）。
func (e *Engine) swapArbLoop(ctx context.Context) {
	if e.quotes == nil {
		return
	}
	// D-469c S3：facts 写后 kick 主路（mt_swap/funding 三源→factsKick 即到即评）+
	// ticker 降 5min 断流兜底（facts 本身 5min-1h 刷新，盲扫 60s 无新数据）。
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	acc := newComboAccumulator()
	scan := func() {
		// D-080 S4 N9 修订：判死策略停扫改为停提交——扫描+评估常开喂 rollup
		e.scanSwapArb(ctx, acc)
		if err := acc.Flush(ctx, e.store); err != nil {
			log.Printf("[autotrade] swapArb rollup flush: %v", err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.factsKick:
			scan()
		case <-ticker.C:
			scan()
		}
	}
}

// scanSwapArb 自动扫描所有跨账户 swap 组合，找净 swap 最优的组合开仓。
// D-072：风控由 gate 统一处理，本函数只产 TradeIntent。
// B：组合平仓后重入冷却——该 ComboKey 存在 CLOSED 持仓且 closed_at > now() − N 天 → 跳过。
// H4.1：满仓短路——CountOpenPositions >= MaxPositions 整轮跳过（省逐组合产 intent 撞 gate 的日志噪音）。
// D-080 R7：acc 由调用方传入（每 loop 一个长生命周期实例，轮末 flush 覆盖写）。
func (e *Engine) scanSwapArb(ctx context.Context, acc *comboAccumulator) {
	if e.maxPositionsReached(ctx) {
		return
	}
	combos := e.discoverSwapCombos(ctx)
	if len(combos) == 0 {
		return
	}
	// B：拉 CLOSED 持仓构建冷却集合（ComboKey → 最近 closed_at）
	cooldownSet := e.swapArbCooldownSet(ctx)
	// D-256-A S4：组合级止损计数（reason-aware）——按 ComboKey 归并 autotrade_logs 止损行。
	comboLossCounts := e.swapArbComboLossCounts(ctx)
	for _, c := range combos {
		ck := combo.Key(c.strategy, c.canonical, c.vLong, c.vShort)
		// D-080 S4：全组合喂入 rollup 累积器（含冷却中/低于门槛——覆盖度完整）
		acc.Record(ck, c.strategy, c.netSwapBps, time.Now())
		// D-256-A S5：复活扳机告警——settled-only，netSwapBps >= 阈值 → critical 告警。
		// 在 cooldown/loss 拦截之前，确保 disabled 策略仍被扫描评估（雷达有人值守）。
		e.maybeRevivalAlert(ck, c.strategy, c.netSwapBps, c.swapSrc)
		if closedAt, blocked := cooldownSet[ck]; blocked {
			log.Printf("[autotrade] %s skip %s: reentry cooldown (closed %s ago)", c.strategy, ck, time.Since(closedAt).Round(time.Hour))
			continue
		}
		// D-256-A S4：组合级止损计数 ≥ limit → skip（reason-aware，排除人工/probe）。
		// D-256-A R2：查表键剥策略前缀（与 swapArbComboLossCounts 归并键一致），跨标签保护。
		if e.cfg.SwapArbComboLossLimit > 0 && comboLossCounts != nil {
			lossKey := stripStrategyPrefix(ck)
			if cnt, hit := comboLossCounts[lossKey]; hit && cnt >= int64(e.cfg.SwapArbComboLossLimit) {
				log.Printf("[autotrade] %s skip %s: combo stop loss count %d >= %d (window %dd)", c.strategy, ck, cnt, e.cfg.SwapArbComboLossLimit, e.cfg.SwapArbComboLossWindowDays)
				continue
			}
		}
		e.trySwapArb(ctx, c.strategy, c.canonical, c.vLong, c.vShort, c.netSwapBps, c.basisBps, c.basisAvailable, c.swapSrc)
	}
}

// swapArbComboLossCounts/buildCooldownSet/maybeRevivalAlert/stripStrategyPrefix/swapArbCooldownSet
// 已提取至 engine_swaparb_gates.go（D-261 S3 纯搬家拆分）。

// trySwapArb 尝试开仓单个 swap 套利组合（D-072：名义额=broker 余额驱动，经 gate）。
// D-169 P0 S2：basisBps/basisAvailable 由 discoverSwapCombos 传入，供 S3b 入场基差门禁消费。
// D-180 S2：swapSrc 由 discoverSwapCombos 传入（settled|spec|none），供 S4 门禁折扣判定 + 决策日志。
func (e *Engine) trySwapArb(ctx context.Context, strategy, canonical, venueLong, venueShort string, netSwapBps, basisBps float64, basisAvailable bool, swapSrc string) {
	// D-080 S4 N9 修订：判死策略停提交（扫描+评估常开喂 rollup，禁用只拦提交无 REJECTED 行）
	// D-190：err → log + return（fail-closed，与 gate.go:75 一致）
	disabled, err := e.store.IsStrategyDisabled(ctx, strategy)
	if err != nil {
		log.Printf("[autotrade] IsStrategyDisabled error (fail-closed skip): %v", err)
		return
	}
	if disabled {
		return
	}
	qLong, okL := e.quotes.Latest(canonical, venueLong)
	qShort, okS := e.quotes.Latest(canonical, venueShort)
	if !okL || !okS {
		log.Printf("[autotrade] %s skip %s: blind leg rejected (quote missing, long=%v short=%v)", strategy, canonical, okL, okS)
		return
	}
	now := time.Now()
	// D-077 修复⑪B N6：陈旧度复用 DetectorFreshnessTTL（env 化）
	if e.cfg.FreshnessTTL > 0 {
		if now.Sub(qLong.Time) > e.cfg.FreshnessTTL || now.Sub(qShort.Time) > e.cfg.FreshnessTTL {
			return
		}
	}
	// D-159 W4a R7：两腿 bid-ask spread 宽度门禁——任一腿 spread 过宽=流动性异常/报价不一致 → skip。
	if tooWide, reason := legSpreadTooWide(qLong, qShort, e.cfg.SwapArbMaxLegSpreadBps); tooWide {
		log.Printf("[autotrade] %s skip %s: %s", strategy, canonical, reason)
		return
	}
	// D-169 P0 S3b：入场基差合理性门禁——盲腿 fail-closed 拒绝；|basisBps| > max 拒绝
	//（正=虚假价差/卖腿比买腿贵，负=基差已偏离/对冲不良）。max<=0 不校验（向后兼容）。
	if reject, reason := entryBasisGateCheck(basisBps, basisAvailable, e.cfg.EntryBasisMaxBps); reject {
		log.Printf("[autotrade] %s skip %s: entry basis gate (%s, basisBps=%.2f, maxBps=%.2f)", strategy, canonical, reason, basisBps, e.cfg.EntryBasisMaxBps)
		return
	}
	spread := qShort.Bid - qLong.Ask
	spreadBps := spread / qLong.Ask * 10000
	// D-077 修复⑪B N6：入场成本容忍 env 化（ARB_SWAPARB_ENTRY_SPREAD_MIN_BPS，默认 -3）
	if spreadBps < e.cfg.EntrySpreadMinBps {
		log.Printf("[autotrade] %s skip %s: spreadBps=%.2f < %.2f", strategy, canonical, spreadBps, e.cfg.EntrySpreadMinBps)
		return
	}
	// D-169 P0 S3c：入场价差上限——spreadBps > max 拒绝（"太好的价差"=虚假价差/跨合约不同底层）。
	// max<=0 不校验（向后兼容）；负值=开仓成本不拦截。
	if reject, reason := entrySpreadMaxGateCheck(spreadBps, e.cfg.EntrySpreadMaxBps); reject {
		log.Printf("[autotrade] %s skip %s: entry spread max gate (%s, spreadBps=%.2f, maxBps=%.2f)", strategy, canonical, reason, spreadBps, e.cfg.EntrySpreadMaxBps)
		return
	}
	// D-165 S3：swap facts 零值校验——任一腿 swap facts=0 但 broker 该 venue 有非零 swap 历史→fail-closed 拒绝。
	// 规格声明与实际结算不一致（如 PXBT BTCUSDT facts=0 但实际 -2.21/日）→ 不允许开仓。
	// 查询失败 → fail-closed 拒绝。
	if e.cfg.SwapFactsZeroCheckEnabled {
		if reason := e.swapFactsZeroCheck(ctx, canonical, venueLong, venueShort); reason != "" {
			log.Printf("[autotrade] %s skip %s: %s", strategy, canonical, reason)
			return
		}
	}
	// D-072 §3.3：名义额 = broker 余额驱动（fail-closed 三条件）
	notional, ok, reason := e.service.SwapArbNotional(canonical, venueLong, venueShort)
	if !ok {
		// D-072 资金注入提醒：sizing 不足 → 发布 FundingNeedNotice 到 topic → 前端弹窗（不入 alerts 表）。
		// 每日每组合至多 1 条（内存节流）；非 sizing 原因（venue 离线/币种不符）仅 log 不弹窗。
		// D-097 R2：口径从 "balance" 改 "sizing"（minEq×ratio < minNotional，非旧全款门）。
		if strings.Contains(reason, "sizing") {
			e.maybePublishFundingNeed(canonical, venueLong, venueShort, reason)
		}
		log.Printf("[autotrade] %s skip %s %s/%s: %s", strategy, canonical, venueLong, venueShort, reason)
		return
	}
	// D-180 S2：决策日志带 swap_src（settled|spec）——S4 门禁折扣判定的依据。
	log.Printf("[autotrade] %s eval %s %s/%s: swap_src=%s netSwap=%.4f bps spreadBps=%.2f basisBps=%.2f",
		strategy, canonical, venueLong, venueShort, swapSrc, netSwapBps, spreadBps, basisBps)
	// D-180 S4：入场门禁三条——厚度+回收期往返+集中度。
	// D-181 S2：probe 仓（swapSrc != settled）旁路厚度+回收期（无实测数据不卡厚度），
	// 集中度不旁路（同 venue×symbol 活跃腿 ≥2 仍拒，立项授权稿 §3）。
	willBeProbe := e.cfg.ProbeEnabled && swapSrc != swapSrcSettled
	// 厚度：净息差 ≥ MinNetCarryBps（spec 源 × SpecDiscount 折扣，settled 不折扣）。
	if !willBeProbe {
		if g := carryThicknessGate(netSwapBps, swapSrc, e.cfg.MinNetCarryBps, e.cfg.SpecDiscount); g.reject {
			log.Printf("[autotrade] %s skip %s: %s", strategy, canonical, g.reason)
			return
		}
		// 回收期往返：2×spreadBps / 净息差 ≤ MaxPaybackDays（2×=开+平对称假设）。
		if g := paybackRoundTripGate(spreadBps, netSwapBps, e.cfg.MaxPaybackDays); g.reject {
			log.Printf("[autotrade] %s skip %s: %s", strategy, canonical, g.reason)
			return
		}
	}
	// 集中度：同 venue×symbol 活跃腿 < MaxLegsPerSymbol（471/472 双空单场景拒第三腿）。
	if g := e.concentrationGate(ctx, canonical, venueLong, venueShort, e.cfg.MaxLegsPerSymbol); g.reject {
		log.Printf("[autotrade] %s skip %s: %s", strategy, canonical, g.reason)
		return
	}
	// D-132 T1：venue 级敞口门——两腿 venue SWAPARB 合计裸露 + 新仓 ≤ equity × cap，否则 skip。
	if !e.service.SwapArbVenueExposureOK(ctx, venueLong, venueShort, notional) {
		log.Printf("[autotrade] %s skip %s: venue exposure cap", strategy, canonical)
		return
	}
	// A 修复：qty 定尺寸用 usdPerLot（买腿 ask 价）——原 notional/ask 是 USD÷JPY 币种混算。
	// qty = notional(USD) ÷ usdPerLot(买腿) = USD ÷ (CS×price÷USDJPY) → 手数按计价币正确换算。
	usdPerLotLong, ok := e.service.USDPerLotForLeg(canonical, venueLong, "ask")
	if !ok || usdPerLotLong <= 0 {
		log.Printf("[autotrade] %s skip %s: usdPerLot buy leg unavailable (fx/quote missing)", strategy, canonical)
		return
	}
	qty := notional / usdPerLotLong
	// H4.2：qty 低于买腿最小步 → debug 跳过（品种适配事实，不产 intent 不落 REJECTED）
	if e.service.QtyBelowMinStep(venueLong, qLong.VenueSymbol, qty) {
		log.Printf("[autotrade] %s skip %s: qty %.6f below min step (notional $%.0f too small for %s)",
			strategy, canonical, qty, notional, qLong.VenueSymbol)
		return
	}
	dedupKey := combo.Key(strategy, canonical, venueLong, venueShort)
	// D-142 ④：组合保证金预算观察版告警（只告警不拦截）+ 杠杆不对称提示。best-effort 不影响开仓。
	e.observeMarginBudget(canonical, venueLong, venueShort, notional)
	intent := TradeIntent{
		Strategy:    strategy,
		Canonical:   canonical,
		VenueBuy:    venueLong,
		VenueSell:   venueShort,
		Qty:         qty,
		NotionalUSD: notional,
		DedupKey:    dedupKey,
		NetBps:      spreadBps,
	}
	// D-168 超限折分：Execute 闭包捕获 intent 指针，读 intent.Qty/NotionalUSD
	// （gate 层折分时修改 intent 字段，闭包内读到折分后的值）
	intentPtr := &intent
	intent.Execute = func(ctx context.Context) (string, string, int64) {
		reqID := fmt.Sprintf("swaparb-%d", time.Now().UnixNano())
		status, msg, posID := e.service.ExecuteMeanRevertWithCap(ctx, canonical, venueLong, venueShort, reqID, intentPtr.Qty, spreadBps, intentPtr.NotionalUSD, strategy)
		if status != "FAILED" && posID > 0 {
			// D-080 S1：新持仓直接写组合键（无时间戳），与 opportunities.id 贯通
			_ = e.store.UpdatePositionOppID(ctx, posID, dedupKey)
			// D-179 S2：落盘 notional_usd=双腿口径（intentPtr.NotionalUSD 是单腿口径，×2=双腿合计）。
			// gate 折分后 intentPtr.NotionalUSD 已更新为 AdjustedNotional（gate.go:140），此处读折分后值。
			_ = e.store.UpdatePositionNotional(ctx, posID, intentPtr.NotionalUSD*2)
			// D-087 §3.4：SWAPARB typed 双字段——ExecuteMeanRevertWithCap 已写 EntrySpreadBps，
			// 此处补 SwapBpsDaily + AnnualPct 覆盖写全
			swap := netSwapBps
			annual := netSwapBps * 365 / 100
			meta := pgstore.PositionMeta{
				EntrySpreadBps: &spreadBps,
				SwapBpsDaily:   &swap,
				AnnualPct:      &annual,
			}
			// D-256-A S3：CARRY expected_net_bps 落值——ExpectedUsd CARRY 分支读 netBps（年化百分点），
			// 不写则 expected_net_bps=0 → ExpectedUsd=0 → 结算恒 skip → verification 样本饿死。
			// SWAPARB 不写 NetBps（ExpectedUsd SWAPARB 分支读 swapBpsDaily，不读 netBps）。
			if strategy == "CARRY" {
				netBps := annual
				meta.NetBps = &netBps
			}
			_ = e.store.UpdatePositionMeta(ctx, posID, strategy, meta)
			// D-180 S3：双腿成交后写 expected_entry_spread_bps（成交价口径基准，止损用）。
			// = (buyActual − sellActual) / buyActual × 10000（BUY 成交=ask、SELL 成交=bid）。
			// 口径：entry=成交价(含滑点) / current=报价口径(不含滑点) → PnL 保守(止损偏早=安全)。
			if entryBps, ok := computeEntrySpreadFromLegs(ctx, e.store, posID); ok {
				_ = e.store.UpdateExpectedEntrySpreadBps(ctx, posID, entryBps)
			}
			// D-181 S2：probe 仓标记——任一腿非 settled → note=PROBE。
			if willBeProbe {
				_ = e.store.SetPositionNote(ctx, posID, "PROBE")
				log.Printf("[autotrade] probe pos=%d marked: swap_src=%s (non-settled legs)", posID, swapSrc)
			}
		}
		return status, msg, posID
	}
	e.gate.Submit(ctx, &intent)
}
