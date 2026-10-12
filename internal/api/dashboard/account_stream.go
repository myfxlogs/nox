// account_stream.go D-445 S0：MT5 OnOrderUpdate 余额帧缓存（纯标量——service 层不引 mt5pb）。
// 定位：Refresh 轮询失败/离线的兜底 + 轮询间隙加温；与 Refresh 结果 per-venue 取较新者
// （service_allowrules.fillRuleAcct），非无条件流优先。
package dashboard

import (
	"context"
	"time"

	"arb/internal/ledger/reconcile"
	"arb/internal/store/pgstore"
	"strings"
)

// ExchangeStore 提供 CEX 交易所配置（pgstore.Store 满足）。
// （D-568 行数腾位：自 account_monitor.go 纯移入，零逻辑改。）
type ExchangeStore interface {
	ListExchangeConfigs(ctx context.Context) ([]pgstore.ExchangeConfig, error)
}

// SetFundsHook D-568：注入资金变更通知口（三源汇一；nil 安全）。
func (m *AccountMonitor) SetFundsHook(fn func()) {
	m.mu.Lock()
	m.fundsHook = fn
	m.mu.Unlock()
}

// notifyFunds 锁外触发资金通知（nil-safe）。
func (m *AccountMonitor) notifyFunds() {
	m.mu.RLock()
	fn := m.fundsHook
	m.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

// StreamAcct 流帧缓存条目。
type StreamAcct struct {
	Balance, Equity, Margin, FreeMargin, Profit, MarginLevel float64
	Currency                                                 string
	FrameTime                                                time.Time // 收帧时刻（proto 无服务端 ts）
}

// ObserveStreamAccount 余额帧落缓存（AcctFrameSink 签名同构；frameTime=收帧时刻）。
// D-571 包B：帧六字段同时 patch states[vk] 并脏化发射（补丁语义——venue 分工归属
// 流域；online 不动，由 OnStreamState 流状态事件承担）。Currency 空串不覆盖
// 已有币种（MT5 帧无 Currency 字段）。venue 无账户态则建零值态+Kick(vk) REST 补全。
func (m *AccountMonitor) ObserveStreamAccount(venueKey string, bal, eq, mg, fm, prof, ml float64, ccy string, ts time.Time) {
	m.streamAccts.Store(venueKey, StreamAcct{
		Balance: bal, Equity: eq, Margin: mg, FreeMargin: fm,
		Profit: prof, MarginLevel: ml, Currency: ccy, FrameTime: ts,
	})
	m.mu.Lock()
	st := m.states[venueKey]
	if st.acct == nil {
		// 帧先于任何 REST 到位：建零值账户态（身份锚=venueKey 三段拆分），
		// 描述字段由定向 REST kick 补全。
		v, vt := venueKey, ""
		if i := strings.IndexByte(venueKey, ':'); i >= 0 {
			v, vt = venueKey[:i], venueKey[i+1:]
		}
		st.acct = &reconcile.AccountInfo{VenueKey: venueKey, Venue: v, VenueType: vt, AssetType: "cfd"}
		st.asOf = ts
		m.states[venueKey] = st
		m.dirtyLocked(venueKey)
		// 热修：kick 10s/键防抖——REST 失败时帧风暴曾致 kick→poll 恶性循环（OOM 19:01）。
		kick := m.kickDebounced(venueKey, acctFrameKickMinInterval, m.frameKickAt)
		m.mu.Unlock()
		if kick {
			m.Kick(venueKey)
		}
		m.notifyFunds()
		return
	}
	changed := st.acct.Balance != bal || st.acct.Equity != eq || st.acct.Margin != mg ||
		st.acct.FreeMargin != fm || st.acct.Profit != prof || (ccy != "" && st.acct.Currency != ccy)
	cp := *st.acct // 防共享指针原地改写：复制后 patch
	st.acct = &cp
	st.acct.Balance = bal
	st.acct.Equity = eq
	st.acct.Margin = mg
	st.acct.FreeMargin = fm
	st.acct.Profit = prof
	if ccy != "" {
		st.acct.Currency = ccy
	}
	st.asOf = ts
	m.states[venueKey] = st
	m.dirtyLocked(venueKey)
	m.mu.Unlock()
	if changed {
		m.notifyFunds() // D-568：帧值变→funds 通知
	}
}

// StreamAccount 读某 venueKey 最近流帧（无→false）。
func (m *AccountMonitor) StreamAccount(venueKey string) (StreamAcct, bool) {
	if v, ok := m.streamAccts.Load(venueKey); ok {
		return v.(StreamAcct), true
	}
	return StreamAcct{}, false
}
