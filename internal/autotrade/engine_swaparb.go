package autotrade

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"arb/internal/combo"
	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// swapArbLoop D-072 swap 套利：自动发现跨账户 swap 组合，名义额=broker 余额驱动。
// D-080 R7：每 loop 一个长生命周期 accumulator——当日累计、轮末 flush 覆盖写；
// 日切由 accumulator 内部 pending 队列处理（零定时器，无共享竞态）。
func (e *Engine) swapArbLoop(ctx context.Context) {
	if e.quotes == nil {
		return
	}
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	acc := newComboAccumulator()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// D-080 S4 N9 修订：判死策略停扫改为停提交——扫描+评估常开喂 rollup
			e.scanSwapArb(ctx, acc)
			if err := acc.Flush(ctx, e.store); err != nil {
				log.Printf("[autotrade] swapArb rollup flush: %v", err)
			}
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
	for _, c := range combos {
		ck := combo.Key("SWAPARB", c.canonical, c.vLong, c.vShort)
		// D-080 S4：全组合喂入 rollup 累积器（含冷却中/低于门槛——覆盖度完整）
		acc.Record(ck, "SWAPARB", c.netSwapBps, time.Now())
		if closedAt, blocked := cooldownSet[ck]; blocked {
			log.Printf("[autotrade] SWAPARB skip %s: reentry cooldown (closed %s ago)", ck, time.Since(closedAt).Round(time.Hour))
			continue
		}
		e.trySwapArb(ctx, c.canonical, c.vLong, c.vShort, c.netSwapBps)
	}
}

// swapArbCooldownSet 返回当前处于重入冷却期的 ComboKey 集合（B 修复）。
// 判定：CLOSED 持仓的 combo.FromOppID(oppID) ∈ 冷却期（closed_at > now() − ReentryCooldownDays 天）。
// ReentryCooldownDays=0 → 关闭冷却（返回空集）；查询失败 → 空集（fail-open，不阻断扫描）。
func (e *Engine) swapArbCooldownSet(ctx context.Context) map[string]time.Time {
	if e.cfg.ReentryCooldownDays <= 0 {
		return nil
	}
	closed, err := e.store.ListPositions(ctx, "CLOSED", 200, 0)
	if err != nil {
		log.Printf("[autotrade] swapArbCooldownSet: list closed positions: %v (fail-open)", err)
		return nil
	}
	return buildCooldownSet(closed, e.cfg.ReentryCooldownDays, time.Now())
}

// buildCooldownSet 纯函数：从 CLOSED 持仓列表构建冷却 ComboKey 集合（B 修复，可单测）。
// cutoff = now − cooldownDays 天；closed_at > cutoff 的持仓按 combo.FromOppID(oppID) 入集。
func buildCooldownSet(closed []pgstore.Position, cooldownDays int, now time.Time) map[string]time.Time {
	if cooldownDays <= 0 {
		return nil
	}
	cutoff := now.AddDate(0, 0, -cooldownDays)
	out := make(map[string]time.Time)
	for _, p := range closed {
		if p.ClosedAt == nil || p.OpportunityID == "" {
			continue
		}
		if p.ClosedAt.Before(cutoff) {
			continue // 冷却过期，放行
		}
		ck := combo.FromOppID(p.OpportunityID)
		// 同 ComboKey 多次平仓取最近 closed_at
		if prev, ok := out[ck]; !ok || p.ClosedAt.After(prev) {
			out[ck] = *p.ClosedAt
		}
	}
	return out
}

// swapComboCandidate 自动扫描发现的 swap 套利候选组合。
type swapComboCandidate struct {
	canonical, vLong, vShort string
	netSwapBps               float64
	basisBps                 float64 // D-140 S3：跨 venue 买价-卖价基差 bps（只展示不动排序）
}

// discoverSwapCombos 从 facts 表自动扫描所有跨账户 swap 组合。
// D-072：minNetSwapBps 从 env 读取（ARB_AUTOTRADE_MIN_NET_SWAP_BPS，默认 0.2）。
// D-104 P0：swap facts 新鲜度门禁 fail-closed——now−f.Ts > SwapFactMaxAge → 跳过该 fact + 计数日志。
func (e *Engine) discoverSwapCombos(ctx context.Context) []swapComboCandidate {
	longFacts, err := e.store.LatestFacts(ctx, fact.KindSwapLong, "", "")
	if err != nil || len(longFacts) == 0 {
		return nil
	}
	shortFacts, err := e.store.LatestFacts(ctx, fact.KindSwapShort, "", "")
	if err != nil || len(shortFacts) == 0 {
		return nil
	}
	now := time.Now()
	maxAge := e.cfg.SwapFactMaxAge
	var staleLong, staleShort int
	if maxAge > 0 {
		filtered := longFacts[:0]
		for _, f := range longFacts {
			if now.Sub(f.Ts) > maxAge {
				staleLong++
				continue
			}
			filtered = append(filtered, f)
		}
		longFacts = filtered
		filteredS := shortFacts[:0]
		for _, f := range shortFacts {
			if now.Sub(f.Ts) > maxAge {
				staleShort++
				continue
			}
			filteredS = append(filteredS, f)
		}
		shortFacts = filteredS
		if staleLong > 0 || staleShort > 0 {
			log.Printf("[autotrade] SWAPARB fact freshness gate: skipped %d long + %d short (maxAge=%s)",
				staleLong, staleShort, maxAge)
		}
		if len(longFacts) == 0 || len(shortFacts) == 0 {
			return nil
		}
	}
	type swapKey struct{ canonical, venue string }
	type swapEntry struct {
		value float64
		src   string // "mt5" / "mt4" / etc（D-077 修复⑪B N2）
	}
	shortMap := make(map[swapKey]swapEntry)
	for _, f := range shortFacts {
		shortMap[swapKey{f.Symbol, f.Venue}] = swapEntry{value: f.Value, src: f.Src}
	}
	var combos []swapComboCandidate
	minNetSwapBps := e.cfg.MinNetSwapBps
	// D-077 修复⑪B N5：删除 ==0 → 0.2 二次默认（config 已有 env 默认 0.2）。
	// 显式 0 = 全扫语义恢复（用户可通过 ARB_AUTOTRADE_MIN_NET_SWAP_BPS=0 关闭门槛）。
	for _, lf := range longFacts {
		// D-126 P1：删除 lf.Value<=0 单腿正 swap 过滤——套利看净 swap（long+short），
		// 非单腿。指数/商品/加密 CFD 的 swap_long 通常为负（broker 收过夜费），
		// 旧过滤挡住了所有非 FX 品种。净 swap 门槛由下方 netSwap < minNetSwapBps 守卫。
		for sfKey, sfEntry := range shortMap {
			if sfKey.canonical != lf.Symbol {
				continue
			}
			if sfKey.venue == lf.Venue {
				continue
			}
			netSwap := lf.Value + sfEntry.value
			if netSwap < minNetSwapBps {
				continue
			}
			// D-077 修复⑪B N2：用 fact.Src 拼 venueKey（mt4 账户也能进 swap 套利）
			vLong := lf.Venue + ":" + lf.Src
			vShort := sfKey.venue + ":" + sfEntry.src
			// D-140 S3：基差 = (卖腿 bid − 买腿 ask) / 买腿 ask × 1e4（只展示不动排序）
			// e.quotes nil 时 basis=0（测试无 qbus 注入不 panic）
			basisBps := 0.0
			if e.quotes != nil {
				if qL, ok1 := e.quotes.Latest(lf.Symbol, vLong); ok1 {
					if qS, ok2 := e.quotes.Latest(sfKey.canonical, vShort); ok2 {
						if qL.Ask > 0 {
							basisBps = (qS.Bid - qL.Ask) / qL.Ask * 10000
						}
					}
				}
			}
			combos = append(combos, swapComboCandidate{
				canonical:  lf.Symbol,
				vLong:      vLong,
				vShort:     vShort,
				netSwapBps: netSwap,
				basisBps:   basisBps,
			})
		}
	}
	sort.Slice(combos, func(i, j int) bool {
		return combos[i].netSwapBps > combos[j].netSwapBps
	})
	return combos
}

// trySwapArb 尝试开仓单个 swap 套利组合（D-072：名义额=broker 余额驱动，经 gate）。
func (e *Engine) trySwapArb(ctx context.Context, canonical, venueLong, venueShort string, netSwapBps float64) {
	// D-080 S4 N9 修订：判死策略停提交（扫描+评估常开喂 rollup，禁用只拦提交无 REJECTED 行）
	if disabled, _ := e.store.IsStrategyDisabled(ctx, "SWAPARB"); disabled {
		return
	}
	qLong, okL := e.quotes.Latest(canonical, venueLong)
	qShort, okS := e.quotes.Latest(canonical, venueShort)
	if !okL || !okS {
		return
	}
	now := time.Now()
	// D-077 修复⑪B N6：陈旧度复用 DetectorFreshnessTTL（env 化）
	if e.cfg.FreshnessTTL > 0 {
		if now.Sub(qLong.Time) > e.cfg.FreshnessTTL || now.Sub(qShort.Time) > e.cfg.FreshnessTTL {
			return
		}
	}
	spread := qShort.Bid - qLong.Ask
	spreadBps := spread / qLong.Ask * 10000
	// D-077 修复⑪B N6：入场成本容忍 env 化（ARB_SWAPARB_ENTRY_SPREAD_MIN_BPS，默认 -3）
	if spreadBps < e.cfg.EntrySpreadMinBps {
		log.Printf("[autotrade] SWAPARB skip %s: spreadBps=%.2f < %.2f", canonical, spreadBps, e.cfg.EntrySpreadMinBps)
		return
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
		log.Printf("[autotrade] SWAPARB skip %s %s/%s: %s", canonical, venueLong, venueShort, reason)
		return
	}
	// D-132 T1：venue 级敞口门——两腿 venue SWAPARB 合计裸露 + 新仓 ≤ equity × cap，否则 skip。
	if !e.service.SwapArbVenueExposureOK(ctx, venueLong, venueShort, notional) {
		log.Printf("[autotrade] SWAPARB skip %s: venue exposure cap", canonical)
		return
	}
	// A 修复：qty 定尺寸用 usdPerLot（买腿 ask 价）——原 notional/ask 是 USD÷JPY 币种混算。
	// qty = notional(USD) ÷ usdPerLot(买腿) = USD ÷ (CS×price÷USDJPY) → 手数按计价币正确换算。
	usdPerLotLong, ok := e.service.USDPerLotForLeg(canonical, venueLong, "ask")
	if !ok || usdPerLotLong <= 0 {
		log.Printf("[autotrade] SWAPARB skip %s: usdPerLot buy leg unavailable (fx/quote missing)", canonical)
		return
	}
	qty := notional / usdPerLotLong
	// H4.2：qty 低于买腿最小步 → debug 跳过（品种适配事实，不产 intent 不落 REJECTED）
	if e.service.QtyBelowMinStep(venueLong, qLong.VenueSymbol, qty) {
		log.Printf("[autotrade] SWAPARB skip %s: qty %.6f below min step (notional $%.0f too small for %s)",
			canonical, qty, notional, qLong.VenueSymbol)
		return
	}
	dedupKey := combo.Key("SWAPARB", canonical, venueLong, venueShort)
	intent := TradeIntent{
		Strategy:    "SWAPARB",
		Canonical:   canonical,
		VenueBuy:    venueLong,
		VenueSell:   venueShort,
		Qty:         qty,
		NotionalUSD: notional,
		DedupKey:    dedupKey,
		NetBps:      spreadBps,
		Execute: func(ctx context.Context) (string, string, int64) {
			reqID := fmt.Sprintf("swaparb-%d", time.Now().UnixNano())
			status, msg, posID := e.service.ExecuteMeanRevertWithCap(ctx, canonical, venueLong, venueShort, reqID, qty, spreadBps, notional, "SWAPARB")
			if status != "FAILED" && posID > 0 {
				// D-080 S1：新持仓直接写组合键（无时间戳），与 opportunities.id 贯通
				_ = e.store.UpdatePositionOppID(ctx, posID, dedupKey)
				// D-087 §3.4：SWAPARB typed 双字段——entry_spread（入场 spread）+ swap_bps_daily（日 swap）
				// ExecuteMeanRevertWithCap 内部已写 EntrySpreadBps，此处补 SwapBpsDaily + AnnualPct 覆盖写全
				swap := netSwapBps
				annual := netSwapBps * 365 / 100
				_ = e.store.UpdatePositionMeta(ctx, posID, "SWAPARB", pgstore.PositionMeta{
					EntrySpreadBps: &spreadBps,
					SwapBpsDaily:   &swap,
					AnnualPct:      &annual,
				})
			}
			return status, msg, posID
		},
	}
	e.gate.Submit(ctx, intent)
}
