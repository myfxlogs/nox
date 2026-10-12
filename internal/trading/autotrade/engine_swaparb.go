package autotrade

import (
	"context"
	"log"
	"time"

	"arb/internal/platform/combo"
)

// swapArbLoop D-072 swap 套利：自动发现跨账户 swap 组合，名义额=broker 余额驱动。
// 每 loop 一个长生命周期 accumulator——当日累计、轮末 flush 覆盖写；
// 日切由 accumulator 内部 pending 队列处理（零定时器，无共享竞态）。
// P1#5 分层：全扫触发源=boot / factsKick（mt_swap/WS funding 写后）/
// 订单事件（KickSwapArb 复用 factsKick，容量释放重建 armed）→ armed 候选集；
// quote 帧（武装 canonical 才进 swapQuoteCh）→ 纯内存 precheck → 升 trySwapArb；
// 原 5min 盲扫 ticker 删除，退化为 facts 饥荒看门狗（2h=mt_swap 1h×2 容忍，只告警）。
// armed/lastScan/pending/合流定时器均本协程单写（positionLoop 同构先例）。
func (e *Engine) swapArbLoop(ctx context.Context) {
	if e.quotes == nil {
		return
	}
	watchdog := time.NewTicker(swapFactsWatchdog)
	defer watchdog.Stop()
	acc := newComboAccumulator()
	armed := make(map[string]*armedCombo)
	lastScan := time.Now()
	lastFactsKick := lastScan
	pending := false
	var coalT *time.Timer
	var coalC <-chan time.Time
	armCoalesce := func() {
		if coalT == nil {
			d := kickCoalesce - time.Since(lastScan)
			if d < 0 {
				d = 0
			}
			coalT = time.NewTimer(d)
			coalC = coalT.C
		}
	}
	scan := func() {
		// 修订：判死策略停扫改为停提交——扫描+评估常开喂 rollup
		combos := e.scanSwapArb(ctx, acc)
		if err := acc.Flush(ctx, e.store); err != nil {
			log.Printf("[autotrade] swapArb rollup flush: %v", err)
		}
		lastScan = time.Now()
		pending = false
		armed = make(map[string]*armedCombo, len(combos))
		for _, c := range combos {
			armed[combo.Key(c.strategy, c.canonical, c.vLong, c.vShort)] = &armedCombo{c: c}
		}
		e.publishSwapArmed(armed)
	}
	scan() // boot 全扫武装
	for {
		select {
		case <-ctx.Done():
			if coalT != nil {
				coalT.Stop()
			}
			return
		case <-e.factsKick:
			lastFactsKick = time.Now()
			if time.Since(lastScan) >= kickCoalesce {
				scan()
			} else {
				pending = true // 1s 合流不丢事件（facts 三源可能几秒内连发）
				armCoalesce()
			}
		case <-coalC:
			coalT, coalC = nil, nil
			if pending {
				scan()
			}
		case q := <-e.swapQuoteCh:
			for _, ac := range armed {
				if ac.tried || ac.c.canonical != q.Symbol {
					continue
				}
				if !e.swapQuotePrecheck(&ac.c) {
					continue // 价不合格：纯内存判定，下一帧再看
				}
				ac.tried = true // 无论成败：状态门禁失败由事件重建 armed 兜底
				e.trySwapArb(ctx, ac.c.strategy, ac.c.canonical, ac.c.vLong, ac.c.vShort, ac.c.netSwapBps, ac.c.swapSrc)
			}
		case <-watchdog.C:
			if time.Since(lastFactsKick) > swapFactsWatchdog {
				e.swapArbWatchdogAlert(lastFactsKick)
			}
		}
	}
}

// scanSwapArb 自动扫描所有跨账户 swap 组合，找净 swap 最优的组合开仓。
// 风控由 gate 统一处理，本函数只产 gate.TradeIntent。
// B：组合平仓后重入冷却——该 ComboKey 存在 CLOSED 持仓且 closed_at > now() − N 天 → 跳过。
// H4.1：满仓短路——CountOpenPositions >= MaxPositions 整轮跳过（省逐组合产 intent 撞 gate 的日志噪音）。
// acc 由调用方传入（每 loop 一个长生命周期实例，轮末 flush 覆盖写）。
func (e *Engine) scanSwapArb(ctx context.Context, acc *comboAccumulator) []swapComboCandidate {
	if e.swapScanHook != nil {
		e.swapScanHook()
	}
	if e.maxPositionsReached(ctx) {
		return nil
	}
	combos := e.discoverSwapCombos(ctx)
	if len(combos) == 0 {
		return nil
	}
	// B：拉 CLOSED 持仓构建冷却集合（ComboKey → 最近 closed_at）
	cooldownSet := e.swapArbCooldownSet(ctx)
	// 组合级止损计数（reason-aware）——按 ComboKey 归并 autotrade_logs 止损行。
	comboLossCounts := e.swapArbComboLossCounts(ctx)
	passed := make([]swapComboCandidate, 0, len(combos)) // D-004 P1#5：过 cooldown/loss 的候选供 loop 建 armed
	for _, c := range combos {
		ck := combo.Key(c.strategy, c.canonical, c.vLong, c.vShort)
		// 全组合喂入 rollup 累积器（含冷却中/低于门槛——覆盖度完整）
		acc.Record(ck, c.strategy, c.netSwapBps, time.Now())
		// 复活扳机告警——settled-only，netSwapBps >= 阈值 → critical 告警。
		// 在 cooldown/loss 拦截之前，确保 disabled 策略仍被扫描评估（雷达有人值守）。
		e.maybeRevivalAlert(ck, c.strategy, c.netSwapBps, c.swapSrc)
		if closedAt, blocked := cooldownSet[ck]; blocked {
			log.Printf("[autotrade] %s skip %s: reentry cooldown (closed %s ago)", c.strategy, ck, time.Since(closedAt).Round(time.Hour))
			continue
		}
		// 组合级止损计数 ≥ limit → skip（reason-aware，排除人工/probe）。
		// 查表键剥策略前缀（与 swapArbComboLossCounts 归并键一致），跨标签保护。
		if e.cfg.SwapArbComboLossLimit > 0 && comboLossCounts != nil {
			lossKey := stripStrategyPrefix(ck)
			if cnt, hit := comboLossCounts[lossKey]; hit && cnt >= int64(e.cfg.SwapArbComboLossLimit) {
				log.Printf("[autotrade] %s skip %s: combo stop loss count %d >= %d (window %dd)", c.strategy, ck, cnt, e.cfg.SwapArbComboLossLimit, e.cfg.SwapArbComboLossWindowDays)
				continue
			}
		}
		passed = append(passed, c)
		e.trySwapArb(ctx, c.strategy, c.canonical, c.vLong, c.vShort, c.netSwapBps, c.swapSrc)
	}
	return passed
}

// swapArbComboLossCounts/buildCooldownSet/maybeRevivalAlert/stripStrategyPrefix/swapArbCooldownSet
// 已提取至 engine_swaparb_gates.go（D-261 S3 纯搬家拆分）。
