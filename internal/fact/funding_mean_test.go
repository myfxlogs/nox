// funding_mean_test.go —— D-035：FundingMeanAnnual 经 SQL 聚合读口的 fail-closed 锚。
package fact

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubQuerier MeanWindowQuerier 测试桩（n=0=窗内零样本语义验证）。
type stubQuerier struct {
	avg   float64
	maxTs time.Time
	n     int
	err   error
}

func (s stubQuerier) FundingWindowAvg(_ context.Context, _, _ string, _ time.Time) (float64, time.Time, int, error) {
	return s.avg, s.maxTs, s.n, s.err
}

// TestFundingMeanAnnual_ZeroSamples n=0（窗内零样本）与查询失败 → ok=false（fail-closed，
// 不回落陈旧瞬时）；n>0 → avg/maxTs 透传。
func TestFundingMeanAnnual_ZeroSamples(t *testing.T) {
	ctx := context.Background()
	if _, _, ok := FundingMeanAnnual(ctx, stubQuerier{}, "okx", "XAUUSDT", FundingMeanWindow); ok {
		t.Fatal("n=0 应 ok=false")
	}
	if _, _, ok := FundingMeanAnnual(ctx, stubQuerier{n: 3, err: errors.New("db")}, "okx", "XAUUSDT", FundingMeanWindow); ok {
		t.Fatal("查询错误应 ok=false")
	}
	rate, ts, ok := FundingMeanAnnual(ctx, stubQuerier{avg: 26.4, maxTs: time.Unix(100, 0), n: 3}, "okx", "XAUUSDT", FundingMeanWindow)
	if !ok || rate != 26.4 || !ts.Equal(time.Unix(100, 0)) {
		t.Fatalf("n>0 应透传 avg/ts，got rate=%v ts=%v ok=%v", rate, ts, ok)
	}
}
