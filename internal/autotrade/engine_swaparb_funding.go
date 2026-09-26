package autotrade

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/dashboard"
)

// maybePublishFundingNeed 发布资金注入提醒（D-072）：每日每组合至多 1 条（内存节流）。
// NeedUSD 由 SwapArbFundingNeedUSD 侧算（不改 SwapArbNotional 签名）；topic 未接线时 Service.PublishFundingNeed 静默丢弃。
// D-154 返修②：AliasLong/AliasShort 由 aliasOrKey 填充（同 receiver 复用 D-144 ③，best-effort 回落 venueKey）。
// D-159 W4a S7：CLOSE_ONLY 下不发弹窗（STATE 挂账 D3）——非 RUNNING 直接 return（fail-closed）。
func (e *Engine) maybePublishFundingNeed(canonical, venueLong, venueShort, reason string) {
	// D-159 W4a S7：CLOSE_ONLY 下不发弹窗——非 RUNNING 直接 return。
	state, err := e.store.GetAutotradeState(context.Background())
	if err != nil {
		log.Printf("[autotrade] maybePublishFundingNeed: read state: %v (fail-closed skip)", err)
		return
	}
	if state == nil || state.State != StateRunning {
		return
	}
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
		AliasLong:  e.aliasOrKey(venueLong),
		AliasShort: e.aliasOrKey(venueShort),
	})
	dedupCapHalf(e.fundingNeedSent) // D-508 S5：无界去重集封顶
	e.fundingNeedSent[key] = today
}
