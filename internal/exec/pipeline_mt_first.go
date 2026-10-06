// pipeline_mt_first.go —— D-084：XASSET_CARRY/FUNDING_HEDGE 双腿「MT 先行顺序 taker」。
// 背景：mtapi 桥「能看不能动」间歇性 REQUEST_ERROR（开仓 8 次中 5 次拒，D-407 结论）；
// 并发双发时每次 MT 拒单都已付 CEX 往返费（pos #508 LEGGED unwind 净损 $4.25 实拍）。
// 改序：MT 腿先成交再发 CEX 腿——MT 拒单零成本（CEX 腿零触达），MT 成交后 CEX taker
// 紧跟（裸腿窗口与并发同量级）。maker 序优先级不变（makerPlan 命中仍走 maker）。
package exec

import (
	"context"
	"log"
)

// mtFirstEligible D-084：mt-first 适用面——开关开 + posType∈{XASSET_CARRY,FUNDING_HEDGE}
// + 恰双腿 + 恰一 MT 腿一非 MT 腿（SWAPARB/单腿/双 CEX/双 MT 全走旧并发路径）。
func (p *Pipeline) mtFirstEligible(posType string, legs []LegSpec) bool {
	if !p.mtFirst || (posType != "XASSET_CARRY" && posType != "FUNDING_HEDGE") || len(legs) != 2 {
		return false
	}
	mt, other := 0, 0
	for _, l := range legs {
		if isMTVenueKey(l.VenueKey) {
			mt++
		} else {
			other++
		}
	}
	return mt == 1 && other == 1
}

// runMTFirstSequence MT 先行顺序下单：先 executeLeg MT 腿，FILLED 才发 CEX 腿。
// 返回与并发路径同形的 results（共享后续判定/落库/LEGGED unwind/UNKNOWN CLOSING）。
// MT FAILED → CEX 腿 PENDING 不发（零成本拒单，终态 filled==0 → FAILED）；
// MT UNKNOWN（超时，可能已成交）→ CEX 同样不发——保守选择，由既有 CLOSING/
// uncertainty+对账链接管（此时补发 CEX 可能在 MT 已成交时把对冲腿送进孤儿态）。
func (p *Pipeline) runMTFirstSequence(ctx context.Context, requestID string, posID int64, legs []LegSpec) []LegResult {
	results := make([]LegResult, len(legs))
	mi := 0
	for i := range results {
		// 未发腿显式 PENDING（未尝试语义，同 D-018 runMakerSequence——空串撞
		// position_legs_status_check；零触达须留审计行）。
		results[i] = LegResult{VenueKey: legs[i].VenueKey, Status: "PENDING"}
		if isMTVenueKey(legs[i].VenueKey) {
			mi = i
		}
	}
	// D-195-B §4.3：client_order_id 确定性编码（attempt_no=1 首次），同并发路径。
	results[mi] = p.executeLeg(ctx, legs[mi], generateClientOrderID(requestID, posID, int16(mi), 1))
	if results[mi].Status != "FILLED" {
		if results[mi].Status == "UNKNOWN" {
			log.Printf("[exec] mt-first: MT leg %s UNKNOWN（可能已成交，CEX 腿不发由对账接管）", legs[mi].VenueKey)
		} else {
			log.Printf("[exec] mt-first: MT leg failed, CEX leg not sent")
		}
		return results
	}
	for i, leg := range legs {
		if i == mi {
			continue
		}
		results[i] = p.executeLeg(ctx, leg, generateClientOrderID(requestID, posID, int16(i), 1))
	}
	return results
}
