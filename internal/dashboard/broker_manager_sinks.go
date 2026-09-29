// D-581：BrokerManager 晚接 sink 晚绑定化——三 sink + OrderProfitKick 目标经 Set* 注入，
// adapter 持有 dispatch* bound-method（调用时 sinkMu.RLock 取最新目标、锁外调用可重入），
// 接线时序不敏感。修复「setupMonitors 赋值晚于 StartAll→adapter 拷到 nil 永久丢帧」。
package dashboard

import (
	"time"

	"arb/internal/adapter"
	"arb/internal/reconcile"
)

// Set* 晚接目标（D-581）：接线方调用，Lock 写目标；adapter 持有 dispatch* 免时序。
func (m *BrokerManager) SetAccountSink(f adapter.AcctFrameSink) {
	m.sinkMu.Lock()
	m.accountSink = f
	m.sinkMu.Unlock()
}
func (m *BrokerManager) SetPositionsSink(f func(string, []reconcile.VenueOrder)) {
	m.sinkMu.Lock()
	m.positionsSink = f
	m.sinkMu.Unlock()
}
func (m *BrokerManager) SetStreamStateSink(f func(string, bool)) {
	m.sinkMu.Lock()
	m.streamStateSink = f
	m.sinkMu.Unlock()
}
func (m *BrokerManager) SetOrderProfitKick(f func()) {
	m.sinkMu.Lock()
	m.orderProfitKick = f
	m.sinkMu.Unlock()
}

// *Fn getter（D-581 命名机械护栏——旧 `brokerMgr.X` 写法编译期报错）：
// RLock 取目标引用返回，调用在锁外（sink 可重入 Kick，锁内调会死锁）。
func (m *BrokerManager) StreamStateSinkFn() func(string, bool) {
	m.sinkMu.RLock()
	defer m.sinkMu.RUnlock()
	return m.streamStateSink
}
func (m *BrokerManager) OrderProfitKickFn() func() {
	m.sinkMu.RLock()
	defer m.sinkMu.RUnlock()
	return m.orderProfitKick
}

// dispatch* bound-method（D-581）：startMT5 拷贝到 adapter；调用时取最新目标，
// 目标未接=no-op=原 nil 语义。锁内只取引用不放行调用。
func (m *BrokerManager) dispatchAccount(vk string, bal, eq, mg, fm, prof, ml float64, ccy string, ts time.Time) {
	m.sinkMu.RLock()
	f := m.accountSink
	m.sinkMu.RUnlock()
	if f != nil {
		f(vk, bal, eq, mg, fm, prof, ml, ccy, ts)
	}
}
func (m *BrokerManager) dispatchPositions(vk string, orders []reconcile.VenueOrder) {
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
