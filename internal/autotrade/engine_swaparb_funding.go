package autotrade

import (
	"fmt"
	"time"

	"arb/internal/dashboard"
)

// maybePublishFundingNeed 发布资金注入提醒（D-072）：每日每组合至多 1 条（内存节流）。
// NeedUSD 由 SwapArbFundingNeedUSD 侧算（不改 SwapArbNotional 签名）；topic 未接线时 Service.PublishFundingNeed 静默丢弃。
func (e *Engine) maybePublishFundingNeed(canonical, venueLong, venueShort, reason string) {
	key := fmt.Sprintf("SWAPARB:%s:%s:%s", canonical, venueLong, venueShort)
	today := time.Now().Format("2006-01-02")
	if e.fundingNeedSent[key] == today {
		return
	}
	needUSD, _ := e.service.SwapArbFundingNeedUSD(canonical, venueLong, venueShort)
	e.service.PublishFundingNeed(dashboard.FundingNeedNotice{
		Canonical:  canonical,
		VenueLong:  venueLong,
		VenueShort: venueShort,
		Reason:     reason,
		NeedUSD:    needUSD,
		Ts:         time.Now(),
	})
	e.fundingNeedSent[key] = today
}
