// d249_quote_bus_race_test.go —— D-249 S2 批2：QuoteBus close/send 竞态测试（先红后绿）。
// 审计 P1-3：Subscribe cancel 的 close(ch) 与 Publish 的 send 赛跑 → panic on closed channel。
// Topic 同款修法：subEntry.mu 同步 close/send + sync.Once + closed 标志。
package bus

import (
	"sync"
	"testing"
)

// TestD249_QuoteBusCloseSendRace 并发 Publish × cancel → 不 panic（subEntry.mu 同步 close/send）。
func TestD249_QuoteBusCloseSendRace(t *testing.T) {
	bus := New()
	_, cancel := bus.Subscribe("AUDJPY")

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				bus.Publish(Quote{Symbol: "AUDJPY", Bid: float64(n*1000 + j), Ask: float64(n*1000 + j + 1)})
			}
		}(i)
	}
	cancelWG := sync.WaitGroup{}
	cancelWG.Add(1)
	go func() {
		defer cancelWG.Done()
		cancel()
	}()
	cancelWG.Wait()
	wg.Wait()
}

// TestD249_QuoteBusDoubleCancel 重复 cancel 不 panic（sync.Once 保护 close）。
func TestD249_QuoteBusDoubleCancel(t *testing.T) {
	bus := New()
	_, cancel := bus.Subscribe("AUDJPY")
	cancel()
	cancel()
}

// TestD249_QuoteBusPublishAfterCancel cancel 后 Publish 不 panic（closed 标志跳过 send）。
func TestD249_QuoteBusPublishAfterCancel(t *testing.T) {
	bus := New()
	_, cancel := bus.Subscribe("AUDJPY")
	cancel()
	bus.Publish(Quote{Symbol: "AUDJPY", Bid: 1.0, Ask: 1.1})
}
