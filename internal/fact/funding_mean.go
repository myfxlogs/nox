// funding_mean.go —— funding 滚动均值年化（D-027 口径单一实现，D-034 起
// autotrade 引擎与 scan universe 共用，禁止第二份求和循环）。
package fact

import (
	"context"
	"time"
)

// FundingMeanWindow D-027/D-034 共用回看窗：7d 滚动均值（含周末零样本=诚实日均）。
const FundingMeanWindow = 7 * 24 * time.Hour

// MeanWindowQuerier facts 窗口读取窄面（pgstore.FactsWithin 原生满足）。
type MeanWindowQuerier interface {
	FactsWithin(ctx context.Context, kind, venue, symbol string, since time.Time) ([]Fact, error)
}

// FundingMeanAnnual (venue, symbol) funding 窗内滚动均值年化 %——value 已是年化
// 百分点口径（采集侧落库即年化），均值含周末零样本；ts=窗内最新样本时戳（供
// 新鲜度闸）。窗内零样本或查询失败 → ok=false（fail-closed，不回落陈旧瞬时）。
func FundingMeanAnnual(ctx context.Context, q MeanWindowQuerier, venue, symbol string, window time.Duration) (rate float64, ts time.Time, ok bool) {
	facts, err := q.FactsWithin(ctx, KindFunding, venue, symbol, time.Now().Add(-window))
	if err != nil || len(facts) == 0 {
		return 0, time.Time{}, false
	}
	var sum float64
	var latest time.Time
	for _, f := range facts {
		sum += f.Value
		if f.Ts.After(latest) {
			latest = f.Ts
		}
	}
	return sum / float64(len(facts)), latest, true
}
