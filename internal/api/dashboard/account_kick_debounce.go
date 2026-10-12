// account_kick_debounce.go 热修 hotfix-loops：kick 防抖 + CEX querier 配置哈希
// （OOM 19:01 实证：无防抖帧风暴 kick→poll 恶性循环；每 poll 全量重建 querier+REST）。
package dashboard

import (
	"fmt"
	"hash/fnv"
	"sync/atomic"
	"time"

	"nox/internal/store/pgstore"
)

const (
	// acctFrameKickMinInterval 流帧 nil-acct 定向 kick 防抖窗（每 venueKey 独立）。
	acctFrameKickMinInterval = 10 * time.Second
	// acctLedgerKickMinInterval CEX 账本事件补描述 kick 防抖窗（账本帧自带数据，kick 仅补字段）。
	acctLedgerKickMinInterval = 2 * time.Second
)

// cexQuerierBuildTotal CEX querier 重建计数（测试断言「配置未变不重建」用）。
var cexQuerierBuildTotal atomic.Int64

// kickDebounced 持 m.mu 期间判定：vk 距上次 kick ≥min 才重记并放行。
// 返回 true 时调用方在锁外 m.Kick(vk)。
func (m *AccountMonitor) kickDebounced(vk string, min time.Duration, at map[string]time.Time) bool {
	if last, ok := at[vk]; ok && time.Since(last) < min {
		return false
	}
	at[vk] = time.Now()
	return true
}

// exchConfigsHash 按影响 querier 构造的全部字段做稳定哈希（顺序敏感=配置序参与）。
func exchConfigsHash(live []pgstore.ExchangeConfig) uint64 {
	h := fnv.New64a()
	for i := range live {
		c := &live[i]
		fmt.Fprintf(h, "%d|%s|%s|%s|%s|%s|%s|%t|%s\x00",
			c.ID, c.Venue, c.VenueType, c.APIKey, c.APISecret, c.Passphrase, c.BaseURL, c.Simulated, c.Website)
	}
	return h.Sum64()
}
