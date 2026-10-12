package swapledger

import (
	"context"
	"log"
	"time"

	"arb/internal/data/fact"
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

// TripleSwapDay 返回指定 venue+canonical 的三倍 swap 日（D-077 修复⑪A M3）。
// 优先级：symbol 级 Fact（symbol=canonical）> venue 级 Fact（symbol=venue）> fail-closed。
func (r *FactTripleDayResolver) TripleSwapDay(venue, canonical string) (time.Weekday, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	// 1. 先查 symbol 级（per-symbol Fact，M3 不一致时发）
	symFacts, err := r.factStore.LatestFacts(ctx, fact.KindTripleSwapDay, venue, canonical)
	if err == nil && len(symFacts) > 0 {
		return time.Weekday(int(symFacts[0].Value)), true
	}
	// 2. 再查 venue 级（symbol=venue 名，一致时发）
	venueFacts, err := r.factStore.LatestFacts(ctx, fact.KindTripleSwapDay, venue, venue)
	if err != nil {
		log.Printf("[swapledger] triple swap day query %s/%s: %v", venue, canonical, err)
		return 0, false
	}
	if len(venueFacts) == 0 {
		// 也查不限 symbol 的兜底（兼容旧数据 symbol="" ）
		allFacts, err := r.factStore.LatestFacts(ctx, fact.KindTripleSwapDay, venue, "")
		if err != nil || len(allFacts) == 0 {
			return 0, false
		}
		return time.Weekday(int(allFacts[0].Value)), true
	}
	return time.Weekday(int(venueFacts[0].Value)), true
}
