// D-182 S3 拆分：从 swap_settlement_profile.go 提取跳变检测函数。
package dashboard

import (
	"arb/internal/keydomain"
	"context"
	"time"

	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
)

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
	bareVenue := keydomain.BareOf(o.VenueKey)
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
	bareVenue := keydomain.BareOf(venueKey)
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
