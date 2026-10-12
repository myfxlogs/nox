package dashboard

// D-118 结算日历感知：预期 swap = dailySwap × (结算天数 + 2×三倍日数)。
// 本文件含三倍日预取 + 结算日历纯函数（周末排除 + 21:00 UTC 边界）。

import (
	"context"
	"time"

	"nox/internal/data/fact"
	"nox/internal/store/pgstore"

	v1 "nox/internal/gen/dashboardv1"
)

// prefetchTripleSwapDays D-118：从活跃持仓的 OPEN legs 提取 venue 列表，批量查 facts 表 triple_swap_day。
// 返回 venueKey→time.Weekday map；未查到/查询失败 → 该 venue 不在 map 中（enrichOne 视为 tripleDay 不存在，退化为纯结算天数）。
func (s *Service) prefetchTripleSwapDays(ctx context.Context, pbPositions []*v1.Position, legsByPos map[int64][]pgstore.PositionLeg) map[string]time.Weekday {
	out := make(map[string]time.Weekday)
	if s.store == nil {
		return out // 测试未接线 → 空 map（enrichOne 退化为纯结算天数）
	}
	// 提取活跃持仓的 OPEN legs venue 集合
	venues := make(map[string]bool)
	for _, pb := range pbPositions {
		if !activeStatuses[pb.Status] || !carryTypes[pb.Type] {
			continue
		}
		for _, leg := range legsByPos[pb.Id] {
			if leg.Phase == "OPEN" {
				venues[leg.Venue] = true
			}
		}
	}
	// 逐 venue 查 triple_swap_day（venue 级，D-496 起账号级 fact symbol=NULL，传 '' wildcard 命中）
	// facts 表 venue 存纯数字名（如 "REDACTED_MT_ACCT"），legs venue 存 "REDACTED_MT_ACCT:mt5" → 去后缀查
	for venueKey := range venues {
		venueName := venueFromKey(venueKey)
		facts, err := s.store.LatestFacts(ctx, fact.KindTripleSwapDay, venueName, "")
		if err == nil && len(facts) > 0 {
			out[venueKey] = time.Weekday(int(facts[0].Value))
		}
		// 查询失败/无数据 → 不入 map（enrichOne 视为无三倍日）
	}
	return out
}

// pickTripleDay D-118：从 OPEN legs 的 venue 中取三倍日（任一腿有三倍日即用，多腿一致时取第一个）。
// 返回 (weekday, found)；tripleDayByVenue 无该 venue → found=false（退化为纯结算天数）。
func pickTripleDay(openLegs []pgstore.PositionLeg, tripleDayByVenue map[string]time.Weekday) (time.Weekday, bool) {
	for _, leg := range openLegs {
		if td, ok := tripleDayByVenue[leg.Venue]; ok {
			return td, true
		}
	}
	return 0, false
}

// openedAtTime D-118：从 pb.OpenedAt 或 OPEN FILLED legs 的最早 ts 取开仓时间。
// 与 holdDaysToNow 同构（提取公共部分，避免重复逻辑）。
func openedAtTime(pb *v1.Position, openLegs []pgstore.PositionLeg) time.Time {
	if pb.OpenedAt != nil {
		return pb.OpenedAt.AsTime()
	}
	var minTs time.Time
	for _, l := range openLegs {
		if l.Status == "FILLED" {
			if minTs.IsZero() || l.Ts.Before(minTs) {
				minTs = l.Ts
			}
		}
	}
	return minTs
}

// settlementDays D-118：计算 [openedAt, now) 区间的结算天数 + 三倍日数。
// 结算天数 = Mon-Fri 天数（排除周六周日，D-110 P1 确认周末不结算）。
// 三倍日数 = 该区间内 weekday==tripleDay 的天数（hasTriple=false 时为 0）。
// 算法：逐日遍历 openedAt 的日期部分到 now 的日期部分（不含 now 当天，结算发生在 UTC 21:00，当天未到结算时间不计）。
// 注意：这里按 UTC 日期遍历（MT5 swap 结算时间 21:00 UTC，日期边界按 UTC）。
func settlementDays(openedAt, now time.Time, tripleDay time.Weekday, hasTriple bool) (settleDays, tripleDays int) {
	// 按 UTC 日期对齐（结算发生在 21:00 UTC，日期按 UTC 算）
	start := openedAt.UTC()
	end := now.UTC()
	// 日期部分（截断到 00:00 UTC）
	startDate := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	endDate := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	// 如果开仓当天已过 21:00 UTC，从次日开始算（当天已结算）
	if start.Hour() >= 21 {
		startDate = startDate.AddDate(0, 0, 1)
	}
	// 如果 now 当天未到 21:00 UTC，不算 now 当天（尚未结算）
	if end.Hour() < 21 {
		endDate = endDate.AddDate(0, 0, -1)
	}
	// 逐日遍历 [startDate, endDate]
	for d := startDate; !d.After(endDate); d = d.AddDate(0, 0, 1) {
		wd := d.Weekday()
		if wd == time.Saturday || wd == time.Sunday {
			continue // 周末不结算
		}
		settleDays++
		if hasTriple && wd == tripleDay {
			tripleDays++
		}
	}
	return settleDays, tripleDays
}
