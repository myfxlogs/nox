// D-180 S4：SWAPARB 入场门禁三条——厚度+回收期往返+集中度。
// 厚度：净息差 ≥ MinNetCarryBps（spec 源 × SpecDiscount 折扣，settled 不折扣）。
// 回收期往返：2×spreadBps / 净息差 ≤ MaxPaybackDays（2×=开+平对称假设，注释钉死）。
// 集中度：同 venue×symbol 活跃腿 < MaxLegsPerSymbol（471/472 双空单场景拒第三腿）。
package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"

	"arb/internal/bus"
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
	if idx := strings.Index(venue, ":"); idx > 0 {
		return venue[:idx]
	}
	return venue
}
