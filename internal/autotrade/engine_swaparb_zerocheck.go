// D-182 S3 拆分：从 engine_swaparb.go 提取 swapFactsZeroCheck（D-165 S3 零值校验）。
package autotrade

import (
	"arb/internal/keydomain"
	"context"
	"fmt"

	"arb/internal/fact"
)

// swapFactsZeroCheck D-165 S3：swap facts 零值校验——任一腿 swap facts=0 但 broker 该 venue
// 有非零 swap 历史→返回拒绝原因（fail-closed）。facts!=0 或无非零历史→放行（空串）。
// 查询失败→返回 fail-closed 原因。venueLong/venueShort 是 venueKey（含 :mt5 后缀），取冒号前 bare venue。
func (e *Engine) swapFactsZeroCheck(ctx context.Context, canonical, venueLong, venueShort string) string {
	longBare := keydomain.BareOf(venueLong)
	shortBare := keydomain.BareOf(venueShort)
	longFacts, err := e.store.LatestFacts(ctx, fact.KindSwapLong, "", canonical)
	if err != nil {
		return fmt.Sprintf("swap facts zero check: long facts query failed (fail-closed): %v", err)
	}
	shortFacts, err := e.store.LatestFacts(ctx, fact.KindSwapShort, "", canonical)
	if err != nil {
		return fmt.Sprintf("swap facts zero check: short facts query failed (fail-closed): %v", err)
	}
	var swapLong, swapShort float64
	longFound, shortFound := false, false
	for _, f := range longFacts {
		if f.Venue == longBare {
			swapLong = f.Value
			longFound = true
		}
	}
	for _, f := range shortFacts {
		if f.Venue == shortBare {
			swapShort = f.Value
			shortFound = true
		}
	}
	if !longFound || !shortFound {
		return "swap facts zero check: venue fact missing (fail-closed)"
	}
	// 检查零值腿是否有非零 swap 历史
	// D-189 P0-1：快照查询必须用完整 venueKey（position_swap_snapshots.venue 是 venueKey），
	// bare venue 仅用于上面 facts 的 f.Venue 匹配（facts.venue 是 bare）。
	if swapLong == 0 {
		has, err := e.store.HasNonZeroSwapHistory(ctx, venueLong)
		if err != nil {
			return fmt.Sprintf("swap facts zero check: has non-zero history long failed (fail-closed): %v", err)
		}
		if has {
			return fmt.Sprintf("swap facts zero but broker has non-zero swap history (venue=%s, facts=0 but broker实际收 swap)", venueLong)
		}
	}
	if swapShort == 0 {
		has, err := e.store.HasNonZeroSwapHistory(ctx, venueShort)
		if err != nil {
			return fmt.Sprintf("swap facts zero check: has non-zero history short failed (fail-closed): %v", err)
		}
		if has {
			return fmt.Sprintf("swap facts zero but broker has non-zero swap history (venue=%s, facts=0 but broker实际收 swap)", venueShort)
		}
	}
	return "" // 放行
}
