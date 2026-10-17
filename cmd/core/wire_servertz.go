// D-337R2 S1：server 时区 fetcher 适配（原 wireServerTZ 收敛——tzCache 已改 NewBrokerPositionMonitor
// 构造必选注入，忘接=编译失败；setter 已删。fetcher 每次调用经 BrokerManager.ServerTZQueriers
// 现取，跟随 attach/detach）。
package main

import (
	"context"
	"sync"
	"time"

	"nox/internal/api/dashboard"
)

func serverTZFetcherFor(bm *dashboard.BrokerManager) dashboard.ServerTZFetcher {
	return func(ctx context.Context, venueKey string) (float64, error) {
		q, ok := bm.ServerTZQueriers()[venueKey]
		if !ok {
			return 0, context.Canceled // 非 MT5/未 attach → 本轮诚实空态
		}
		return q.ServerTimezone(ctx)
	}
}

// swapVenueWeekdayHolder D-597 时序审计病灶①：swapledger 三倍日 weekday 判定需
// venue server 日期——runServices 构造序早于 brokerMgr 作用域，延迟绑定收口
// （xassetAllowRef 同范式 D-017 R1）。main 构造 cache 后 Bind；Func() 未 Bind=nil
// → swapledger UTC 回落+留痕（非静默）。
type swapVenueWeekdayHolder struct {
	mu sync.Mutex
	fn func(venueKey string, t time.Time) (time.Weekday, bool)
}

func (h *swapVenueWeekdayHolder) Bind(c dashboard.ServerTZCache) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fn = func(venueKey string, t time.Time) (time.Weekday, bool) {
		loc := c.Get(context.Background(), venueKey)
		if loc == nil {
			return t.Weekday(), false // fetch 败/冷却——诚实不可得，回落方处置
		}
		return t.In(loc).Weekday(), true
	}
}

func (h *swapVenueWeekdayHolder) Func() func(venueKey string, t time.Time) (time.Weekday, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fn
}

var swapVenueWeekday swapVenueWeekdayHolder
