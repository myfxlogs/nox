// D-180 S1：结算画像采集——swap 快照跳变检测 → 聚合中位数 → 写 swap_settled_long/short facts。
// 实时链路搭车 collectSwapSnapshots（事件驱动，无新定时器）；boot 存量回溯扫全历史。
// 画像单位 = venue×symbol：同 (venue, symbol, side, 结算日UTC) 多仓跳变取中位数归一条 fact。
package dashboard

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"

	"arb/internal/fact"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
)

// SettlementProfileStore D-180 S1：结算画像采集所需 store 接口。
// pgstore.Store 实现此接口；测试可注入 fake。
type SettlementProfileStore interface {
	PositionLegLister
	LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error)
	InsertFacts(ctx context.Context, facts []fact.Fact) error
	FactExistsAt(ctx context.Context, kind, venue, symbol string, ts time.Time) (bool, error)
	ListAllSwapSnapshots(ctx context.Context) ([]pgstore.SwapSnapshot, error)
	ListSwapSnapshotsByPosID(ctx context.Context, posID int64) ([]pgstore.SwapSnapshot, error) // D-182 S1
}

// swapJump D-180 S1：单仓单次结算跳变。
type swapJump struct {
	venue         string // bare venue（不含 :mt5 后缀）
	symbol        string // canonical
	side          string // BUY / SELL
	settlementDay string // "2006-01-02" UTC
	usdPerLot     float64
	ts            time.Time
}

// SettlementProfileCollector D-180 S1：结算画像采集器。
// 实时链路由 BrokerPositionMonitor.collectSwapSnapshots 调用 RecordJump；
// boot 存量回溯由 BackfillFromHistory 一次性扫全历史。
type SettlementProfileCollector struct {
	store SettlementProfileStore
}

// NewSettlementProfileCollector 创建采集器。
func NewSettlementProfileCollector(store SettlementProfileStore) *SettlementProfileCollector {
	return &SettlementProfileCollector{store: store}
}

// RecordJumps D-180 S1：实时链路——收集本轮 poll 检测到的跳变，聚合写 facts。
// 调用方（collectSwapSnapshots）在 poll 末尾调一次，传入本轮全部跳变。
// 跳变已由调用方检测（current.swap - prev.swap），此处只做聚合+幂等写。
func (c *SettlementProfileCollector) RecordJumps(ctx context.Context, jumps []swapJump) {
	if c == nil || c.store == nil || len(jumps) == 0 {
		return
	}
	filtered := c.filterTripleSwapDays(ctx, jumps)
	if len(filtered) == 0 {
		return
	}
	facts := aggregateJumpsToFacts(filtered)
	c.writeFactsIdempotent(ctx, facts)
}

// BackfillFromHistory D-180 S1 T-4②：boot 存量回溯——扫 position_swap_snapshots 全历史跳变，
// 同公式（UTC 日+三倍日剔除+聚合中位数+幂等）回填 facts。幂等保证不重复。
// 跳变检测逻辑与实时链路共用 detectJumpsFromSnapshots。
func (c *SettlementProfileCollector) BackfillFromHistory(ctx context.Context) error {
	if c == nil || c.store == nil {
		return nil
	}
	snaps, err := c.store.ListAllSwapSnapshots(ctx)
	if err != nil {
		return err
	}
	if len(snaps) == 0 {
		return nil
	}
	// 按 (position_id, venue) 分组检测跳变
	legsCache := make(map[int64][]pgstore.PositionLeg)
	var allJumps []swapJump
	groupStart := 0
	for i := 1; i <= len(snaps); i++ {
		sameGroup := i < len(snaps) &&
			snaps[i].PositionID == snaps[groupStart].PositionID &&
			snaps[i].Venue == snaps[groupStart].Venue
		if !sameGroup {
			group := snaps[groupStart:i]
			groupStart = i
			if len(group) < 2 {
				continue
			}
			// 取 legs（缓存）
			posID := group[0].PositionID
			legs, ok := legsCache[posID]
			if !ok {
				legs, _ = c.store.ListPositionLegs(ctx, posID)
				legsCache[posID] = legs
			}
			jumps := detectJumpsFromSnapshots(group, legs, group[0].Venue)
			allJumps = append(allJumps, jumps...)
			// D-182 S1：同分组零检测——恒零 ≥48h → settled=0（不结算族实测零表达）
			c.detectAndRecordZero(ctx, group, legs, group[0].Venue)
		}
	}
	if len(allJumps) == 0 {
		return nil
	}
	filtered := c.filterTripleSwapDays(ctx, allJumps)
	if len(filtered) == 0 {
		return nil
	}
	facts := aggregateJumpsToFacts(filtered)
	c.writeFactsIdempotent(ctx, facts)
	log.Printf("[settlement-profile] backfill: %d snapshots → %d jumps → %d facts", len(snaps), len(allJumps), len(facts))
	return nil
}

// filterTripleSwapDays 剔除三倍日跳变（查 triple_swap_day fact，命中 → skip）。
func (c *SettlementProfileCollector) filterTripleSwapDays(ctx context.Context, jumps []swapJump) []swapJump {
	var out []swapJump
	for _, j := range jumps {
		if c.isTripleSwapDay(ctx, j.venue, j.symbol, j.ts) {
			log.Printf("[settlement-profile] skip triple swap day: venue=%s symbol=%s day=%s",
				j.venue, j.symbol, j.settlementDay)
			continue
		}
		out = append(out, j)
	}
	return out
}

// isTripleSwapDay 查 triple_swap_day fact，判断 day 是否为三倍日。
// 优先级：symbol 级 > venue 级（symbol=venue）> 不限 symbol 兜底。
func (c *SettlementProfileCollector) isTripleSwapDay(ctx context.Context, venue, symbol string, day time.Time) bool {
	// 1. symbol 级
	facts, _ := c.store.LatestFacts(ctx, fact.KindTripleSwapDay, venue, symbol)
	if len(facts) > 0 {
		return int(facts[0].Value) == int(day.UTC().Weekday())
	}
	// 2. venue 级（symbol=venue）
	facts, _ = c.store.LatestFacts(ctx, fact.KindTripleSwapDay, venue, venue)
	if len(facts) > 0 {
		return int(facts[0].Value) == int(day.UTC().Weekday())
	}
	// 3. 不限 symbol 兜底
	facts, _ = c.store.LatestFacts(ctx, fact.KindTripleSwapDay, venue, "")
	if len(facts) > 0 {
		return int(facts[0].Value) == int(day.UTC().Weekday())
	}
	return false
}

// detectRealtimeJump D-180 S1：实时链路跳变检测——从 in-memory prev→current 构造 swapJump。
// 从 store 查 legs 获取 side/symbol/qty，jumpUSD = current.swap - prev.swap，usdPerLot = jumpUSD / qty。
// 首次写入（hasPrev=false）不算跳变；qty<=0 或 leg 缺失 → ok=false。
func detectRealtimeJump(store PositionLegLister, posID int64, o reconcile.VenueOrder, prevSwap float64, now time.Time) (swapJump, bool) {
	legs, err := store.ListPositionLegs(context.Background(), posID)
	if err != nil {
		return swapJump{}, false
	}
	var leg *pgstore.PositionLeg
	for i := range legs {
		if legs[i].Phase == "OPEN" && legs[i].Venue == o.VenueKey {
			leg = &legs[i]
			break
		}
	}
	if leg == nil || leg.Qty <= 0 {
		return swapJump{}, false
	}
	jumpUSD := o.Swap - prevSwap
	if jumpUSD == 0 {
		return swapJump{}, false
	}
	bareVenue := strings.Split(o.VenueKey, ":")[0]
	return swapJump{
		venue:         bareVenue,
		symbol:        leg.Symbol,
		side:          leg.Side,
		settlementDay: now.UTC().Format("2006-01-02"),
		usdPerLot:     jumpUSD / leg.Qty,
		ts:            now,
	}, true
}

// detectJumpsFromSnapshots D-180 S1：从一组有序快照（同 position_id+venue，ts 升序）检测跳变。
// 跳变 = 相邻快照 swap_usd 差值 ≠ 0。side/symbol/qty 从 legs 取（匹配 venue）。
// 返回 swapJump 列表（usdPerLot = jumpUSD / qty）。
// 共用函数：实时链路（单步 prev→current）与 boot 回溯（全历史）都用此函数的变体。
func detectJumpsFromSnapshots(snaps []pgstore.SwapSnapshot, legs []pgstore.PositionLeg, venueKey string) []swapJump {
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
	bareVenue := strings.Split(venueKey, ":")[0]
	var jumps []swapJump
	for i := 1; i < len(snaps); i++ {
		jumpUSD := snaps[i].SwapUSD - snaps[i-1].SwapUSD
		if jumpUSD == 0 {
			continue
		}
		settlementDay := snaps[i].Ts.UTC().Format("2006-01-02")
		jumps = append(jumps, swapJump{
			venue:         bareVenue,
			symbol:        leg.Symbol,
			side:          leg.Side,
			settlementDay: settlementDay,
			usdPerLot:     jumpUSD / leg.Qty,
			ts:            snaps[i].Ts,
		})
	}
	return jumps
}

// aggregateJumpsToFacts D-180 S1：按 (venue, symbol, side, settlementDay) 聚合跳变取中位数 → fact。
// kind = swap_settled_long (BUY) / swap_settled_short (SELL)。
// ts = 结算日 UTC 00:00（幂等键组成部分）。
func aggregateJumpsToFacts(jumps []swapJump) []fact.Fact {
	type groupKey struct {
		venue, symbol, side, day string
	}
	groups := make(map[groupKey][]float64)
	groupTs := make(map[groupKey]time.Time)
	for _, j := range jumps {
		k := groupKey{j.venue, j.symbol, j.side, j.settlementDay}
		groups[k] = append(groups[k], j.usdPerLot)
		if existing, ok := groupTs[k]; !ok || j.ts.Before(existing) {
			groupTs[k] = j.ts
		}
	}
	var facts []fact.Fact
	for k, values := range groups {
		kind := fact.KindSwapSettledLong
		if k.side != "BUY" {
			kind = fact.KindSwapSettledShort
		}
		dayStart := parseDayStart(k.day)
		facts = append(facts, fact.Fact{
			Kind:   kind,
			Venue:  k.venue,
			Symbol: k.symbol,
			Value:  median(values),
			Unit:   fact.UnitUsdLotDay,
			Ts:     dayStart,
			Src:    "swap_snapshots",
		})
	}
	return facts
}

// writeFactsIdempotent 幂等写 facts：逐条检查 FactExistsAt，已存在则跳过。
func (c *SettlementProfileCollector) writeFactsIdempotent(ctx context.Context, facts []fact.Fact) {
	var toWrite []fact.Fact
	for _, f := range facts {
		exists, err := c.store.FactExistsAt(ctx, f.Kind, f.Venue, f.Symbol, f.Ts)
		if err != nil {
			log.Printf("[settlement-profile] fact exists check: %v (skip write)", err)
			continue
		}
		if exists {
			continue
		}
		toWrite = append(toWrite, f)
	}
	if len(toWrite) == 0 {
		return
	}
	if err := c.store.InsertFacts(ctx, toWrite); err != nil {
		log.Printf("[settlement-profile] insert facts: %v", err)
		return
	}
	log.Printf("[settlement-profile] wrote %d facts (skipped %d duplicates)", len(toWrite), len(facts)-len(toWrite))
}

// median 中位数（排序后取中间值，偶数取两值平均）。
func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// parseDayStart "2006-01-02" → time.Time（UTC 00:00）。
func parseDayStart(day string) time.Time {
	t, _ := time.Parse("2006-01-02", day)
	return t.UTC()
}
