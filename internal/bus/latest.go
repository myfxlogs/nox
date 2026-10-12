package bus

import "time"

// LatestQuote 返回 venues 中 Time 最新且未超陈旧 TTL 的一帧（G3，拒绝陈旧帧冒充现价）。
// 无报价或全部陈旧 → 返回 zero Quote 和 false。dashboard 与 position 共用（D-049，P3 单点）。
func LatestQuote(venues map[string]Quote, freshTTL time.Duration, now time.Time) (Quote, bool) {
	var best Quote
	found := false
	for _, q := range venues {
		if now.Sub(q.Time) > freshTTL {
			continue // 陈旧帧丢弃
		}
		if !found || q.Time.After(best.Time) {
			best = q
			found = true
		}
	}
	return best, found
}
