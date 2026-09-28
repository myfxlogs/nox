// balance_ledger.go —— D-542 S1：CEX 账户可用资金活账本（内存态主源）。
// 私有 WS 事件喂入（binance ACCOUNT_UPDATE/outboundAccountPosition、okx account、
// gate futures.balances）；REST 校准结果同驻（src=rest，liveRestTTL 内有效）。
// 重连标脏→读取方脏/空走一发 REST 校准；快照表降级为历史账兜底。
// nil 接收器安全——未装配时全部 no-op/未命中。
package bus

import (
	"sync"
	"time"
)

// BalanceEntry 单池活账本条目（可用+净值+原币+时点+来源）。
type BalanceEntry struct {
	VenueKey  string
	Available float64
	Equity    float64
	Currency  string
	At        time.Time
	Src       string // "ws"=私有流事件 / "rest"=实拉校准
	dirty     bool
}

// Dirty 返回条目是否被标脏（读取方用：脏→REST 校准）。
func (e BalanceEntry) Dirty() bool { return e.dirty }

// BalanceLedger venue_key→账户池内存态（三 venue 同构一通道）。
type BalanceLedger struct {
	mu sync.RWMutex
	m  map[string]BalanceEntry
}

// NewBalanceLedger 创建活账本。
func NewBalanceLedger() *BalanceLedger { return &BalanceLedger{m: map[string]BalanceEntry{}} }

// Put 写/覆盖条目（流事件或 REST 校准同源入口；写即解脏）。
func (l *BalanceLedger) Put(venueKey string, avail, equity float64, ccy, src string, at time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.m[venueKey] = BalanceEntry{VenueKey: venueKey, Available: avail, Equity: equity, Currency: ccy, At: at, Src: src}
	l.mu.Unlock()
}

// MarkDirty 标脏（私有流每轮重连前调用——死流/断链期读取方走 REST 校准不读尸值）。
func (l *BalanceLedger) MarkDirty(venueKey string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if e, ok := l.m[venueKey]; ok {
		e.dirty = true
		l.m[venueKey] = e
	}
	l.mu.Unlock()
}

// Get 干净条目：存在 && 非脏 &&（src=ws 直读 / src=rest 未超 restTTL）。
// rest 条目超期视为脏——下一发实拉前不读过期校准值。
func (l *BalanceLedger) Get(venueKey string, restTTL time.Duration, now time.Time) (BalanceEntry, bool) {
	if l == nil {
		return BalanceEntry{}, false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	e, ok := l.m[venueKey]
	if !ok || e.dirty {
		return BalanceEntry{}, false
	}
	if e.Src == "rest" && now.Sub(e.At) > restTTL {
		return BalanceEntry{}, false
	}
	return e, true
}
