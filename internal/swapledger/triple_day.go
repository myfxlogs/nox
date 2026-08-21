package swapledger

import (
	"context"
	"log"
	"time"

	"arb/internal/fact"
)

// FactTripleDayResolver 从 facts 表读 triple_swap_day（D-077 V2）。
// 每次 TripleSwapDay 调用查库（日频 ticker 调用频率低，无需缓存）。
type FactTripleDayResolver struct {
	factStore factStore
	timeout   time.Duration
}

// factStore facts 查询接口（pgstore.Store 实现）。
type factStore interface {
	LatestFacts(ctx context.Context, kind, venue, symbol string) ([]fact.Fact, error)
}

// NewFactTripleDayResolver 创建从 facts 表读三倍日的 resolver。
func NewFactTripleDayResolver(store factStore) *FactTripleDayResolver {
	return &FactTripleDayResolver{factStore: store, timeout: 5 * time.Second}
}

// TripleSwapDay 返回指定 venue 的三倍 swap 日（D-077 V2）。
// 从 facts 表查 kind=triple_swap_day, venue=指定 venue 的最新行。
// ok=false 表示未采集到（下游回退周三假设）。
func (r *FactTripleDayResolver) TripleSwapDay(venue string) (time.Weekday, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	facts, err := r.factStore.LatestFacts(ctx, fact.KindTripleSwapDay, venue, "")
	if err != nil {
		log.Printf("[swapledger] triple swap day query %s: %v", venue, err)
		return 0, false
	}
	if len(facts) == 0 {
		return 0, false
	}
	return time.Weekday(int(facts[0].Value)), true
}
