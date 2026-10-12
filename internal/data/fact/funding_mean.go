// funding_mean.go —— funding 滚动均值年化（D-027 口径单一实现，D-034 起
// autotrade 引擎与 scan universe 共用，禁止第二份求和循环；D-035 起均值由
// SQL 单条聚合完成，Go 侧零行搬运）。
package fact

import (
	"context"
	"time"
)

// FundingMeanWindow D-027/D-034 共用回看窗：7d 滚动均值（含周末零样本=诚实日均）。
const FundingMeanWindow = 7 * 24 * time.Hour

// MeanWindowQuerier funding 窗聚合窄面（pgstore.FundingWindowAvg 原生满足）。
// 返回窗内样本均值/最新时戳/样本数——n=0 即窗内零样本，由调用方判 fail-closed。
type MeanWindowQuerier interface {
	FundingWindowAvg(ctx context.Context, venue, symbol string, since time.Time) (avg float64, maxTs time.Time, n int, err error)
}

// FundingMeanAnnual (venue, symbol) funding 窗内滚动均值年化 %——value 已是年化
// 百分点口径（采集侧落库即年化），均值含周末零样本；ts=窗内最新样本时戳（供
// 新鲜度闸）。窗内零样本或查询失败 → ok=false（fail-closed，不回落陈旧瞬时）。
func FundingMeanAnnual(ctx context.Context, q MeanWindowQuerier, venue, symbol string, window time.Duration) (rate float64, ts time.Time, ok bool) {
	avg, maxTs, n, err := q.FundingWindowAvg(ctx, venue, symbol, time.Now().Add(-window))
	if err != nil || n == 0 {
		return 0, time.Time{}, false
	}
	return avg, maxTs, true
}
