// account_stream.go D-445 S0：MT5 OnOrderUpdate 余额帧缓存（纯标量——service 层不引 mt5pb）。
// 定位：Refresh 轮询失败/离线的兜底 + 轮询间隙加温；与 Refresh 结果 per-venue 取较新者
// （service_allowrules.fillRuleAcct），非无条件流优先。
package dashboard

import "time"

// StreamAcct 流帧缓存条目。
type StreamAcct struct {
	Balance, Equity, Margin, FreeMargin, Profit, MarginLevel float64
	Currency                                                 string
	FrameTime                                                time.Time // 收帧时刻（proto 无服务端 ts）
}

// ObserveStreamAccount 余额帧落缓存（AcctFrameSink 签名同构；frameTime=收帧时刻）。
func (m *AccountMonitor) ObserveStreamAccount(venueKey string, bal, eq, mg, fm, prof, ml float64, ccy string, ts time.Time) {
	m.streamAccts.Store(venueKey, StreamAcct{
		Balance: bal, Equity: eq, Margin: mg, FreeMargin: fm,
		Profit: prof, MarginLevel: ml, Currency: ccy, FrameTime: ts,
	})
}

// StreamAccount 读某 venueKey 最近流帧（无→false）。
func (m *AccountMonitor) StreamAccount(venueKey string) (StreamAcct, bool) {
	if v, ok := m.streamAccts.Load(venueKey); ok {
		return v.(StreamAcct), true
	}
	return StreamAcct{}, false
}
