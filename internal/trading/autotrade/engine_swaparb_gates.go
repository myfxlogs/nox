// SWAPARB 入场门禁三条——厚度+回收期往返+集中度。
// 厚度：净息差 ≥ MinNetCarryBps（spec 源 × SpecDiscount 折扣，settled 不折扣）。
// 回收期往返：2×spreadBps / 净息差 ≤ MaxPaybackDays（2×=开+平对称假设，注释钉死）。
// 集中度：同 venue×symbol 活跃腿 < MaxLegsPerSymbol（471/472 双空单场景拒第三腿）。
package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/data/bus"
	"arb/internal/data/keydomain"
	"arb/internal/platform/combo"
	"arb/internal/store/pgstore"
)

// legSpreadTooWide D-159 W4a R7：两腿 bid-ask spread 宽度门禁纯函数。
// maxBps<=0 → 不校验（返回 false）；任一腿 mid<=0 或 spread>maxBps → 返回 (true, reason)。
// reason 用于 log 跳过原因（调用方拼 canonical）。
func legSpreadTooWide(qLong, qShort bus.Quote, maxBps float64) (bool, string) {
	if maxBps <= 0 {
		return false, ""
	}
	longMid := qLong.Mid()
	shortMid := qShort.Mid()
	if longMid <= 0 || shortMid <= 0 {
		return true, fmt.Sprintf("mid<=0 (longMid=%.6f, shortMid=%.6f)", longMid, shortMid)
	}
	longSpreadBps := (qLong.Ask - qLong.Bid) / longMid * 10000
	shortSpreadBps := (qShort.Ask - qShort.Bid) / shortMid * 10000
	if longSpreadBps > maxBps || shortSpreadBps > maxBps {
		return true, fmt.Sprintf("leg spread too wide (long=%.2f bps, short=%.2f bps, max=%.1f)",
			longSpreadBps, shortSpreadBps, maxBps)
	}
	return false, ""
}

// entryBasisGateCheck D-169 P0 S3b：入场基差合理性门禁纯函数（可单测）。
// maxBps<=0 → 不校验（向后兼容）；basisAvailable=false（盲腿）→ fail-closed 拒绝；
// |basisBps| > maxBps → 拒绝（正=虚假价差，负=基差已偏离）。
func entryBasisGateCheck(basisBps float64, basisAvailable bool, maxBps float64) (bool, string) {
	if maxBps <= 0 {
		return false, ""
	}
	if !basisAvailable {
		return true, "blind leg"
	}
	absBps := basisBps
	if absBps < 0 {
		absBps = -absBps
	}
	if absBps > maxBps {
		return true, "divergence"
	}
	return false, ""
}

// entrySpreadMaxGateCheck D-169 P0 S3c：入场价差上限纯函数（可单测）。
// maxBps<=0 → 不校验（向后兼容）；spreadBps > maxBps → 拒绝（"太好的价差"=虚假价差）。
func entrySpreadMaxGateCheck(spreadBps, maxBps float64) (bool, string) {
	if maxBps <= 0 {
		return false, ""
	}
	if spreadBps > maxBps {
		return true, "too good"
	}
	return false, ""
}

// swapArbGateResult D-180 S4：门禁判定结果。
type swapArbGateResult struct {
	reject bool
	reason string
}

// carryThicknessGate D-180 S4 厚度门禁：净息差 ≥ MinNetCarryBps（spec 源 × SpecDiscount 折扣）。
// netSwapBps 来自 swapRateFor（settled 优先 → spec 兜底）；swapSrc=spec 时 ×折扣（冷启动不裸信规格）。
// settled 源不折扣（实测已含真实摩擦）；none 源已在 discoverSwapCombos fail-closed 拦截，不进此门禁。
// MinNetCarryBps<=0 → 不校验（向后兼容）。
func carryThicknessGate(netSwapBps float64, swapSrc string, minNetCarryBps, specDiscount float64) swapArbGateResult {
	if minNetCarryBps <= 0 {
		return swapArbGateResult{}
	}
	// spec 源折扣（仅入场门禁用，止损/展示用原值）
	effectiveNetSwap := swapSrcDiscount(netSwapBps, swapSrc, specDiscount)
	if effectiveNetSwap < minNetCarryBps {
		return swapArbGateResult{
			reject: true,
			reason: fmt.Sprintf("carry thickness: netSwap %.4f bps (src=%s, eff=%.4f after discount) < %.2f",
				netSwapBps, swapSrc, effectiveNetSwap, minNetCarryBps),
		}
	}
	return swapArbGateResult{}
}

// paybackRoundTripGate D-180 S4 回收期往返门禁：2×spreadBps / 净息差 ≤ MaxPaybackDays。
// 2×=开+平对称假设（开仓付 spreadBps，平仓再付 spreadBps，注释钉死）。
// spreadBps 来自 trySwapArb 已算值（qShort.Bid − qLong.Ask）/qLong.Ask×10000。
// 净息差用原值（不折扣——回收期是物理时间，折扣只作用厚度门禁的"是否值得开"判定）。
// 净息差 ≤0 → 拒绝（无回收期语义）；MaxPaybackDays<=0 → 不校验（向后兼容）。
func paybackRoundTripGate(spreadBps, netSwapBps float64, maxPaybackDays int) swapArbGateResult {
	if maxPaybackDays <= 0 {
		return swapArbGateResult{}
	}
	if netSwapBps <= 0 {
		return swapArbGateResult{
			reject: true,
			reason: fmt.Sprintf("payback: netSwap %.4f bps <= 0 (no carry to recoup spread)", netSwapBps),
		}
	}
	// 回收期（天）= 2×spreadBps / netSwapBps（bps/日）
	// 2×=开+平对称假设：开仓付 spreadBps 一次，平仓再付 spreadBps 一次。
	paybackDays := 2 * spreadBps / netSwapBps
	if paybackDays > float64(maxPaybackDays) {
		return swapArbGateResult{
			reject: true,
			reason: fmt.Sprintf("payback round-trip: 2×%.2f / %.4f = %.1f days > %d (2×=open+close symmetric)",
				spreadBps, netSwapBps, paybackDays, maxPaybackDays),
		}
	}
	return swapArbGateResult{}
}

// concentrationGate D-180 S4 集中度门禁：同 venue×symbol 活跃腿 < MaxLegsPerSymbol。
// 扫全部活跃仓的 OPEN 腿，按 venue×symbol 计数；新仓两腿各加 1，任一超限 → 拒绝。
// 471/472 场景：61585565 AUDJPY 已有 2 腿（471 空 + 472 空），新仓再加 1 腿 = 3 > 2 → 拒第三腿。
// MaxLegsPerSymbol<=0 → 不校验（向后兼容）。
func (e *Engine) concentrationGate(ctx context.Context, canonical, venueLong, venueShort string, maxLegsPerSymbol int) swapArbGateResult {
	if maxLegsPerSymbol <= 0 {
		return swapArbGateResult{}
	}
	if e.store == nil {
		return swapArbGateResult{}
	}
	// 扫全部活跃仓
	positions, err := e.store.ListActivePositions(ctx)
	if err != nil {
		// fail-open：查询失败不阻塞开仓（与 ListProductGroups fail-open 一致）
		log.Printf("[autotrade] SWAPARB concentration gate: ListActivePositions failed (fail-open): %v", err)
		return swapArbGateResult{}
	}
	// 按 venue×symbol 计数 OPEN 腿
	legCount := make(map[string]int)
	for _, p := range positions {
		if p.Type != "SWAPARB" && p.Type != "CARRY" {
			continue
		}
		legs, err := e.store.ListPositionLegs(ctx, p.ID)
		if err != nil {
			continue
		}
		for _, leg := range legs {
			if leg.Phase != "OPEN" {
				continue
			}
			// venue×symbol 键（bare venue 提取冒号前，与 canonical 对齐）
			bareVenue := legVenueBare(leg.Venue)
			legSymbol := leg.Symbol
			if legSymbol == "" {
				legSymbol = canonicalFromOppID(p.OpportunityID)
			}
			legCount[bareVenue+"\x00"+legSymbol]++
		}
	}
	// 新仓两腿各加 1，检查是否超限
	bareLong := swapSrcFromVenueKey(venueLong)
	bareShort := swapSrcFromVenueKey(venueShort)
	longKey := bareLong + "\x00" + canonical
	shortKey := bareShort + "\x00" + canonical
	if legCount[longKey]+1 > maxLegsPerSymbol {
		return swapArbGateResult{
			reject: true,
			reason: fmt.Sprintf("concentration: venue=%s symbol=%s active legs %d +1 > %d",
				bareLong, canonical, legCount[longKey], maxLegsPerSymbol),
		}
	}
	if legCount[shortKey]+1 > maxLegsPerSymbol {
		return swapArbGateResult{
			reject: true,
			reason: fmt.Sprintf("concentration: venue=%s symbol=%s active legs %d +1 > %d",
				bareShort, canonical, legCount[shortKey], maxLegsPerSymbol),
		}
	}
	return swapArbGateResult{}
}

// legVenueBare 提取 leg.Venue 的 bare venue（冒号前）。
// leg.Venue 格式="venue:mt5"（与 venueKey 一致）；无冒号=原值。
func legVenueBare(venue string) string {
	return keydomain.BareOf(venue)
}

// --- D-261 S3：以下从 engine_swaparb.go 搬入（纯搬家：冷却集 + 止损计数 + 复活告警） ---

// swapArbComboLossCounts D-256-A S4：查 autotrade_logs 止损行按 ComboKey 归并计数。
// 返回 map[ComboKey]count（oppID 经 combo.FromOppID 归一化）。
// 查询失败 → nil（fail-open，不阻断扫描，同 cooldownSet 惯例）。
// SwapArbComboLossLimit=0 或 SwapArbComboLossWindowDays<=0 → nil（关闭）。
func (e *Engine) swapArbComboLossCounts(ctx context.Context) map[string]int64 {
	if e.cfg.SwapArbComboLossLimit <= 0 || e.cfg.SwapArbComboLossWindowDays <= 0 {
		return nil
	}
	oppCounts, err := e.store.CountComboStopLosses(ctx, e.cfg.SwapArbComboLossWindowDays)
	if err != nil {
		log.Printf("[autotrade] swapArbComboLossCounts: query failed (fail-open): %v", err)
		return nil
	}
	out := make(map[string]int64)
	for oppID, cnt := range oppCounts {
		ck := combo.FromOppID(oppID)
		// 剥策略前缀——combo.Key 含 strategy 前缀，CARRY/SWAPARB 标签漂移时
		// 同配对的止损计数键不一致，跨标签循环保护失效。剥首段后键=canonical:venueBuy:venueSell。
		ck = stripStrategyPrefix(ck)
		out[ck] += cnt
	}
	return out
}

// stripStrategyPrefix D-256-A R2：剥 combo.Key 的策略前缀段。
// combo.Key = strategy:canonical:venueBuy:venueSell → canonical:venueBuy:venueSell。
// 无 ":" 的异常输入原样返回（同 combo.FromOppID 防御惯例）。
func stripStrategyPrefix(ck string) string {
	idx := strings.Index(ck, ":")
	if idx < 0 {
		return ck
	}
	return ck[idx+1:]
}

// maybeRevivalAlert D-256-A S5：复活扳机告警（雷达有人值守）。
// 条件：netSwapBps >= SwapArbRevivalBps 且 swapSrc == settled → critical 告警（不自动复活）。
// 节流：in-memory map[comboKey]time.Time，1h/次（重启重置，实时告警语义）。
// AlertSink nil 或 SwapArbRevivalBps <= 0 → no-op。
// spec 源不告警（仅 settled 实测值触发复活评估）。
func (e *Engine) maybeRevivalAlert(ck, strategy string, netSwapBps float64, swapSrc string) {
	if e.AlertSink == nil || e.cfg.SwapArbRevivalBps <= 0 {
		return
	}
	if swapSrc != swapSrcSettled {
		return
	}
	if netSwapBps < e.cfg.SwapArbRevivalBps {
		return
	}
	e.marginAlertMu.Lock()
	defer e.marginAlertMu.Unlock()
	now := time.Now()
	if last, ok := e.revivalAlertSent[ck]; ok && now.Sub(last) < time.Hour {
		return
	}
	dedupCapHalf(e.revivalAlertSent) // D-508 S5：无界去重集封顶
	e.revivalAlertSent[ck] = now
	msg := fmt.Sprintf("[复活评估] %s %s：净息差 %.4fbps ≥ %.2f（已结算口径）——触发复活复审，需业主/决策席裁决",
		strategy, ck, netSwapBps, e.cfg.SwapArbRevivalBps)
	log.Printf("[autotrade] %s", msg)
	e.AlertSink("critical", msg)
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
