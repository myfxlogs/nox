package dashboard

import "nox/internal/data/bus"

// D-633 现态读面 qbus 接入助手——nil 闸（测试席/未装配=fail-closed miss
// 如实拒，禁 panic 顶替）。
func quoteOrMiss(b *bus.QuoteBus, canonical, native, venueKey string) (bus.Quote, bool) {
	if b == nil {
		return bus.Quote{}, false
	}
	return b.LatestLeg(canonical, native, venueKey)
}

func quoteOrNil(b *bus.QuoteBus, symbol, venueKey string) (bus.Quote, bool) {
	if b == nil {
		return bus.Quote{}, false
	}
	return b.Latest(symbol, venueKey)
}
