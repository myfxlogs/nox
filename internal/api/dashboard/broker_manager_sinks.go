// D-581：BrokerManager 晚接 sink 晚绑定化——三 sink + OrderProfitKick 目标经 Set* 注入，
// adapter 持有 dispatch* bound-method（调用时 sinkMu.RLock 取最新目标、锁外调用可重入），
// 接线时序不敏感。修复「setupMonitors 赋值晚于 StartAll→adapter 拷到 nil 永久丢帧」。
package dashboard

import (
	"log"
	"time"

	"nox/internal/data/adapter"
	"nox/internal/data/venue"
)

// Set* 晚接目标（D-581）：接线方调用，Lock 写目标；adapter 持有 dispatch* 免时序。
// SetDealSink D-607：Deal 事件下沉（OnOrderUpdate Update.Deal——非交易族→io 落账）。
func (m *BrokerManager) SetDealSink(f adapter.DealSink) {
	m.sinkMu.Lock()
	m.dealSink = f
	m.sinkMu.Unlock()
}
func (m *BrokerManager) SetAccountSink(f adapter.AcctFrameSink) {
	m.sinkMu.Lock()
	m.accountSink = f
	m.sinkMu.Unlock()
}
func (m *BrokerManager) SetPositionsSink(f func(string, []venue.Order)) {
	m.sinkMu.Lock()
	m.positionsSink = f
	m.sinkMu.Unlock()
}
func (m *BrokerManager) SetStreamStateSink(f func(string, bool)) {
	m.sinkMu.Lock()
	m.streamStateSink = f
	m.sinkMu.Unlock()
}

// SetOrderProfitKick D-071：kick 回调携 venueKey（margin fact.Venue 的完整键）——
// 定向 kick 无防抖直读（boot 竞态修复：boot poll 早于 MT5 attach 时 margin 首采
// 是空仓账户唯一入态事件，走全量防抖会被丢）。
func (m *BrokerManager) SetOrderProfitKick(f func(venueKey string)) {
	m.sinkMu.Lock()
	m.orderProfitKick = f
	m.sinkMu.Unlock()
}

// SetReconKick D-004 P1#6：MT5 会话重连成功 → 对账 kick（venue 断流期事件漏发补救）。
func (m *BrokerManager) SetReconKick(f func()) {
	m.sinkMu.Lock()
	m.reconKick = f
	m.sinkMu.Unlock()
}

// *Fn getter（D-581 命名机械护栏——旧 `brokerMgr.X` 写法编译期报错）：
// RLock 取目标引用返回，调用在锁外（sink 可重入 Kick，锁内调会死锁）。
func (m *BrokerManager) StreamStateSinkFn() func(string, bool) {
	m.sinkMu.RLock()
	defer m.sinkMu.RUnlock()
	return m.streamStateSink
}
func (m *BrokerManager) OrderProfitKickFn() func(venueKey string) {
	m.sinkMu.RLock()
	defer m.sinkMu.RUnlock()
	return m.orderProfitKick
}

// kickAccountReady D-071 R1：adapter ready 点补一次账户定向 kick——margin 首采 kick
// 可早于 ready（15032313 实拍早 11.4s，8514 符号 backfill）→ 直读打空且此后零事件；
// ready=querier 可读权威时点，再 kick 一次（定向无防抖，幂等）。nil 安全。
func (m *BrokerManager) kickAccountReady(venueKey string) {
	if k := m.OrderProfitKickFn(); k != nil {
		k(venueKey)
	}
}

// dispatch* bound-method（D-581）：startMT5 拷贝到 adapter；调用时取最新目标，
// 目标未接=no-op=原 nil 语义。锁内只取引用不放行调用。
// dispatchDeal D-607：Deal 事件透传（sink panic 不拉崩流循环——同 dispatchAccount 先例）。
func (m *BrokerManager) dispatchDeal(vk string, d adapter.DealEvent) {
	m.sinkMu.Lock()
	f := m.dealSink
	m.sinkMu.Unlock()
	if f == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[broker-mgr] deal sink panic: %v", r)
		}
	}()
	f(vk, d)
}

func (m *BrokerManager) dispatchAccount(vk string, bal, eq, mg, fm, prof, ml float64, ccy string, ts time.Time) {
	m.sinkMu.RLock()
	f := m.accountSink
	m.sinkMu.RUnlock()
	if f != nil {
		f(vk, bal, eq, mg, fm, prof, ml, ccy, ts)
	}
}
func (m *BrokerManager) dispatchPositions(vk string, orders []venue.Order) {
	m.sinkMu.RLock()
	f := m.positionsSink
	m.sinkMu.RUnlock()
	if f != nil {
		f(vk, orders)
	}
}
func (m *BrokerManager) dispatchStreamState(vk string, on bool) {
	m.sinkMu.RLock()
	f := m.streamStateSink
	m.sinkMu.RUnlock()
	if f != nil {
		f(vk, on)
	}
}
func (m *BrokerManager) dispatchReconKick() {
	m.sinkMu.RLock()
	f := m.reconKick
	m.sinkMu.RUnlock()
	if f != nil {
		f()
	}
}

// SetAcctResync D-523②：MT5 会话重连成功 → 该键账户定向实读+快照 resync 写口。
func (m *BrokerManager) SetAcctResync(f func(vk string)) {
	m.sinkMu.Lock()
	m.acctResync = f
	m.sinkMu.Unlock()
}
func (m *BrokerManager) dispatchAcctResync(vk string) {
	m.sinkMu.RLock()
	f := m.acctResync
	m.sinkMu.RUnlock()
	if f != nil {
		f(vk)
	}
}
