// D-266 S3：逆回购利率雷达 RPC handler（GetRepoRateRadar）。
package dashboard

import (
	"context"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GetRepoRateRadar D-266 S3：逆回购利率雷达——后端算好，前端零计算（D-043 前端零信任）。
// 字段：4 品种最新利率 + GC007 30d 均值 + GC007 当日 OHLC + 窗口提醒 + 更新时间。
// 冷启动（无数据）→ 全零响应（前端「数据未就绪」），不猜不报错。
func (s *Service) GetRepoRateRadar(ctx context.Context, req *connect.Request[v1.GetRepoRateRadarRequest]) (*connect.Response[v1.GetRepoRateRadarResponse], error) {
	// 1. 取最新 4 品种利率
	rates, err := s.store.LatestRepoRates(ctx)
	if err != nil {
		return nil, err
	}
	var snapshots []*v1.RepoRateSnapshot
	for _, r := range rates {
		snapshots = append(snapshots, &v1.RepoRateSnapshot{
			Code:      r.Code,
			Name:      r.Name,
			RatePct:   r.RatePct,
			ChangePct: r.ChangePct, // *float64 → proto optional 透传 presence
		})
	}

	// 2. GC007 30d 均值（基线）
	gc007Avg, _, err := s.store.RepoRate30dAvg(ctx, "204007")
	if err != nil {
		return nil, err
	}

	// 3. GC007 当日 OHLC（北京时间交易日）
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	nowFn := s.nowFn
	if nowFn == nil {
		nowFn = time.Now // D-347：nil = 生产墙钟
	}
	now := nowFn().In(loc)
	tradeDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	open, close_, high, low, err := s.store.RepoRateDailyOHLC(ctx, "204007", tradeDate)
	if err != nil {
		return nil, err
	}

	// 4. 窗口提醒（季末/年末/节前 3 交易日）
	windowAlert, windowLabel := computeRepoWindow(now)

	// 5. updated_at：取最新 fin_repo_rates 的真实 max(ts)；空表 → nil（不报当前时间）
	var updatedAt *time.Time
	for _, r := range rates {
		if updatedAt == nil || r.Ts.After(*updatedAt) {
			ts := r.Ts
			updatedAt = &ts
		}
	}

	return connect.NewResponse(&v1.GetRepoRateRadarResponse{
		Rates:        snapshots,
		Gc007_30DAvg: gc007Avg,
		Gc007Open:    open,
		Gc007Close:   close_,
		Gc007High:    high,
		Gc007Low:     low,
		WindowAlert:  windowAlert,
		WindowLabel:  windowLabel,
		UpdatedAt:    repoUpdatedAtPB(updatedAt),
	}), nil
}

// repoUpdatedAtPB 将可空更新时间转为 protobuf Timestamp；nil → nil（前端「—」）。
func repoUpdatedAtPB(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// computeRepoWindow 判断是否在季末/年末/节前 3 交易日窗口。
// 季末=3/6/9/12 月最后交易日；年末=12 月最后交易日。
// 简化实现：当月最后 3 个交易日（含今日）→ window_alert=true。
// 节前提醒依赖 cnfi_calendar，此处简化为月末日历判断（前端展示用，告警由采集器判定）。
func computeRepoWindow(now time.Time) (bool, string) {
	loc := now.Location()
	// 月末：当月最后一天
	lastOfMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, loc).Add(-24 * time.Hour)
	daysToMonthEnd := lastOfMonth.Sub(now).Hours() / 24
	if daysToMonthEnd < 0 {
		daysToMonthEnd = 0
	}
	// 季末月（3/6/9/12）最后 3 交易日
	isQuarterEnd := now.Month() == 3 || now.Month() == 6 || now.Month() == 9 || now.Month() == 12
	if isQuarterEnd && daysToMonthEnd < 3 {
		if now.Month() == 12 {
			return true, "年末窗口"
		}
		return true, "季末窗口"
	}
	// 非季末月的月末 3 日也提醒（资金面可能紧张）
	if daysToMonthEnd < 3 {
		return true, "月末窗口"
	}
	return false, ""
}
