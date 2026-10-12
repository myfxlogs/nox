// D-182 S1：实测零表达——不结算族的 settled=0 fact。
// 277 族（Exness 不结算）swap 恒零 → 快照零跳变 → 永无 settled fact → swapRateFor 回落 spec（裸值）→ 误杀。
// 修法：同一 (position, venue) 快照序列跨度 ≥48h 且 swap_usd 恒零 → 写 swap_settled_{long,short} value=0。
// 实测零是有效数据（不结算=实付 0），不同于「无数据」——swapRateFor 命中 settled=0 不再回落 spec。
package dashboard

import (
	"arb/internal/keydomain"
	"context"
	"log"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// zeroSettlementMinSpan 零检测最小跨度（48h=跨过 ≥1 个结算时刻，防新仓早写）。
// 包级 var 便于测试覆写。
var zeroSettlementMinSpan = 48 * time.Hour

// DetectZeroSettlementFacts D-182 S1：从一组有序快照（同 position_id+venue，ts 升序）检测恒零段。
// 恒零段 = 最后一个跳变之后的连续 swap_usd==0 子序列（无跳变则整段）。
// 跨度 ≥48h → 返回 settled=0 fact（settlementDay=末快照 UTC 日）。
// 跳变检测与零检测互斥：有跳变→该腿有结算行为，只看末段恒零（broker 中途停结算场景）。
// 纯函数，boot 回溯与 scanAndClose 周期共用。
func DetectZeroSettlementFacts(snaps []pgstore.SwapSnapshot, legs []pgstore.PositionLeg, venueKey, canonical string) []fact.Fact {
	if len(snaps) < 2 {
		return nil
	}
	// 从 legs 找匹配 venueKey 的 OPEN 腿
	var leg *pgstore.PositionLeg
	for i := range legs {
		if legs[i].Phase == "OPEN" && legs[i].Venue == venueKey {
			leg = &legs[i]
			break
		}
	}
	if leg == nil || leg.Qty <= 0 {
		return nil
	}
	// 找最后一个跳变点（swap_usd 相邻变化）
	lastJump := -1 // -1 = 无跳变
	for i := 1; i < len(snaps); i++ {
		if snaps[i].SwapUSD != snaps[i-1].SwapUSD {
			lastJump = i
		}
	}
	// 恒零段 = 最后跳变之后的连续 swap_usd==0 子序列（无跳变则整段）。
	// lastJump 是跳变落点索引（snaps[lastJump] != snaps[lastJump-1]），
	// 末段从 lastJump 起（含跳变后的新值）；无跳变则从 0 起。
	segStart := 0
	if lastJump >= 0 {
		segStart = lastJump
	}
	zeroSeg := snaps[segStart:]
	// 检查恒零段 swap_usd 全零
	for _, s := range zeroSeg {
		if s.SwapUSD != 0 {
			return nil // 末段非零 → 不是恒零
		}
	}
	// 跨度 ≥48h
	span := zeroSeg[len(zeroSeg)-1].Ts.Sub(zeroSeg[0].Ts)
	if span < zeroSettlementMinSpan {
		return nil
	}
	bareVenue := keydomain.BareOf(venueKey)
	kind := fact.KindSwapSettledLong
	if leg.Side != "BUY" {
		kind = fact.KindSwapSettledShort
	}
	settlementDay := zeroSeg[len(zeroSeg)-1].Ts.UTC().Format("2006-01-02")
	// D-183：settled fact 的 symbol 用 canonical（swapRateFor 按 canonical 查），
	// 非 leg.Symbol（broker 原始符号如 AUDJPYm）——否则 swapRateFor 查不到 settled=0 回落 spec。
	symbol := canonical
	if symbol == "" {
		symbol = leg.Symbol // 回退（不应发生，调用方应传 canonical）
	}
	return []fact.Fact{{
		Kind:   kind,
		Venue:  bareVenue,
		Symbol: symbol,
		Value:  0,
		Unit:   fact.UnitUsdLotDay,
		Ts:     parseDayStart(settlementDay),
		Src:    "swap_snapshots",
	}}
}

// detectAndRecordZero D-182 S1：boot 回溯路径——对一组快照（同 position+venue）检测恒零并写 fact。
// 在 BackfillFromHistory 内对每个分组调用（与跳变检测同分组）。
// D-183：canonical 从 opportunity_id 解析（SWAPARB:CANONICAL:...），settled fact 用 canonical 非 broker 符号。
func (c *SettlementProfileCollector) detectAndRecordZero(ctx context.Context, group []pgstore.SwapSnapshot, legs []pgstore.PositionLeg, venueKey, canonical string) {
	facts := DetectZeroSettlementFacts(group, legs, venueKey, canonical)
	if len(facts) == 0 {
		return
	}
	c.writeFactsIdempotent(ctx, facts)
	log.Printf("[settlement-profile] zero settlement detected: venue=%s symbol=%s side=%s → settled=0 (span≥48h)",
		facts[0].Venue, facts[0].Symbol, legSideFromKind(facts[0].Kind))
}

// RecordZeroForPosition D-182 S1：scanAndClose 周期路径——对单个活跃仓按 posID 拉序列检测恒零。
// 10min 兜底节流已有，对每个 OPEN 仓调用。幂等（writeFactsIdempotent）。
func (c *SettlementProfileCollector) RecordZeroForPosition(ctx context.Context, posID int64) {
	if c == nil || c.store == nil {
		return
	}
	snaps, err := c.store.ListSwapSnapshotsByPosID(ctx, posID)
	if err != nil || len(snaps) == 0 {
		return
	}
	legs, err := c.store.ListPositionLegs(ctx, posID)
	if err != nil {
		return
	}
	// D-183：从 opportunity_id 解析 canonical（SWAPARB:CANONICAL:...），settled fact 用 canonical。
	canonical := canonicalFromPosID(ctx, c.store, posID)
	// 按 venue 分组（snaps 已按 venue, ts 排序）
	groupStart := 0
	for i := 1; i <= len(snaps); i++ {
		sameGroup := i < len(snaps) && snaps[i].Venue == snaps[groupStart].Venue
		if !sameGroup {
			group := snaps[groupStart:i]
			groupStart = i
			if len(group) < 2 {
				continue
			}
			c.detectAndRecordZero(ctx, group, legs, group[0].Venue, canonical)
		}
	}
}

// legSideFromKind 从 fact kind 推断 side（日志用）。
func legSideFromKind(kind string) string {
	if kind == fact.KindSwapSettledLong {
		return "BUY"
	}
	return "SELL"
}

// canonicalFromPosID D-183：从 positions.opportunity_id 解析 canonical。
// opportunity_id 格式 = "SWAPARB:CANONICAL:venueBuy:venueSell"（genID 规则）。
// 查询失败/格式不符 → 返回空（调用方回退 leg.Symbol）。
func canonicalFromPosID(ctx context.Context, store SettlementProfileStore, posID int64) string {
	p, err := store.GetPosition(ctx, posID)
	if err != nil || p == nil || p.OpportunityID == "" {
		return ""
	}
	parts := strings.SplitN(p.OpportunityID, ":", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}
