// account_monitor_events.go D-571 包B：逐 venue 账户事件发射面（dirty 合批 flush + 回放）
// +流帧/账本 patch 入口。镜像 broker_position_events.go：dirty 集合即发射集，
// proto.Equal 在 flush 去重无变帧；cur 为回放源。锁纪律同包A：锁内取快照→锁外构
// proto→回锁写 cur 比对发射。
package dashboard

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"

	"google.golang.org/protobuf/proto"
)

// acctFlushDelay 尾沿合批窗口（镜像 bpFlushDelay；测试可缩）。
var acctFlushDelay = 200 * time.Millisecond

// dirtyLocked 脏化一 venue 并挂尾沿 flush（mu 持锁前提）。
func (m *AccountMonitor) dirtyLocked(vk string) {
	m.dirty[vk] = struct{}{}
	if m.flushT == nil {
		bound := bpFlushBound // D-575：调度时刻同步取值（镜像 bpMon dirtyLocked）
		m.flushT = time.AfterFunc(acctFlushDelay, func() {
			ctx, cancel := context.WithTimeout(context.Background(), bound)
			defer cancel()
			m.flush(ctx)
		})
	}
}

// onlineLocked 生效在线态（mu 持锁前提）：流状态已知 → 流权威；缺席 → REST 成败。
// 传入本次更新的 st（restOnline 就地值）——勿读 m.states 旧条目（先算后存=晚一拍）。
func (m *AccountMonitor) onlineLocked(vk string, st acctVenueState) bool {
	if m.streamKnown[vk] {
		return m.streamOnline[vk]
	}
	return st.restOnline
}

// flush 尾沿合批发射：drain dirty → 逐 vk 构 AccountInfo → proto.Equal 去重 →
// case 16 AccountEvent 发射 + cur 回写（回放源）。acct==nil（纯流状态态，账户未采到）
// 跳过不发行——账户行存在性=有 acct。
func (m *AccountMonitor) flush(ctx context.Context) {
	m.mu.Lock()
	m.flushT = nil
	if len(m.dirty) == 0 {
		m.mu.Unlock()
		return
	}
	keys := make([]string, 0, len(m.dirty))
	for k := range m.dirty {
		keys = append(keys, k)
	}
	m.dirty = make(map[string]struct{})
	states := make(map[string]acctVenueState, len(keys))
	for _, k := range keys {
		states[k] = m.states[k]
	}
	m.mu.Unlock()
	sort.Strings(keys)

	now := time.Now()
	for _, vk := range keys {
		st := states[vk]
		if st.acct == nil {
			continue // 无账户态不发（online 纯事件不产行）
		}
		asOf := st.asOf
		if asOf.IsZero() {
			asOf = now
		}
		pb := accountToProto(st.acct, st.online, asOf)
		m.mu.Lock()
		old := m.cur[vk]
		// UpdatedAt=asOf 每 poll/帧刷新——按值比较须排除时间戳，否则恒不等=每轮全量重发
		// （丢旧保新下游只见最后一条）。值未变时沿用 old（保留原 UpdatedAt 语义=最后真变时刻）。
		changed := true
		if old != nil {
			ts := pb.UpdatedAt
			pb.UpdatedAt = old.UpdatedAt
			changed = !proto.Equal(old, pb)
			pb.UpdatedAt = ts
		}
		if changed {
			m.cur[vk] = pb
		}
		m.mu.Unlock()
		if changed && m.evTopic != nil {
			m.evTopic.Publish(&v1.AccountEvent{Account: pb})
		}
	}
}

// Events 回放源：cur 全量逐条 upsert（新流连接初始化态，service_stream_init 用）。
func (m *AccountMonitor) Events() []*v1.AccountEvent {
	m.mu.RLock()
	out := make([]*v1.AccountEvent, 0, len(m.cur))
	for _, k := range sortedKeys(m.cur) {
		out = append(out, &v1.AccountEvent{Account: m.cur[k]})
	}
	m.mu.RUnlock()
	return out
}

// drainKicks 取走并清空待回读 venue 集（合并语义：集合去重）。镜像 bpMon。
func (m *AccountMonitor) drainKicks() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.pendingKick))
	for vk := range m.pendingKick {
		out = append(out, vk)
	}
	m.pendingKick = make(map[string]struct{})
	return out
}

// OnStreamState venue 流状态事件（StreamStateSink 扇出第二挂点，镜像 bpMon）。
// streamKnown 置位 → online 生效态翻转则 dirty（acct 未到时 flush 跳过不发行）。
func (m *AccountMonitor) OnStreamState(venueKey string, online bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streamKnown[venueKey] = true
	m.streamOnline[venueKey] = online
	st := m.states[venueKey]
	newOnline := m.onlineLocked(venueKey, st)
	if st.online == newOnline {
		return // 生效在线态未翻转（幂等）
	}
	st.online = newOnline
	m.states[venueKey] = st
	m.dirtyLocked(venueKey)
}

// SetBalanceLedger 注入 CEX 余额活账本（main_monitors 装配，OnLedgerAcct 消费面）。
func (m *AccountMonitor) SetBalanceLedger(l *bus.BalanceLedger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.balLedger = l
}

// OnLedgerAcct CEX 私有流余额写账本后回调（ledger.SetOnPut 第二挂点）。
// 命中（非 dirty 条目在窗内）→ patch 六字段等价集+dirty；未命中 → Kick(vk) REST 兜底。
// 键语义钉死：ledger 键=pool 键（binance:perp 等），仅精确等值匹配——撞键 #id 输家
// 不被错喂，无 ledger 条目自然走 REST kick 路径。
func (m *AccountMonitor) OnLedgerAcct(vk string) {
	m.mu.RLock()
	l := m.balLedger
	m.mu.RUnlock()
	e, ok := l.Get(vk, 5*time.Minute, time.Now())
	if !ok {
		m.Kick(vk)
		return
	}
	m.mu.Lock()
	st := m.states[vk]
	if st.acct == nil {
		// 无账户态不建行（身份=查询器集）——kick REST 补全。
		m.mu.Unlock()
		m.Kick(vk)
		return
	}
	cp := *st.acct // 防共享指针原地改写：复制后 patch
	st.acct = &cp
	st.acct.FreeMargin = e.Available
	st.acct.Balance = e.Equity
	st.acct.Equity = e.Equity
	if e.Currency != "" {
		st.acct.Currency = e.Currency
	}
	st.asOf = e.At
	m.states[vk] = st
	m.dirtyLocked(vk)
	m.mu.Unlock()
}

// acctSame 值等比较（D-568 值变才通知口径）：proto 比较排除时间戳/online（运行时态不参与资金语义）。
func acctSame(a, b *reconcile.AccountInfo) bool {
	if a == nil || b == nil {
		return a == b
	}
	pa := accountToProto(a, false, time.Time{})
	pb := accountToProto(b, false, time.Time{})
	return proto.Equal(pa, pb)
}

// resolveExchangeBaseURL 推导 CEX 交易所 base（D-059 抽公共，账户查询与执行器共用）。
// 行为与原 queriers() 内联一致：base_url 显式 → 用之；空 → binance 默认 BINANCE_PROXY_URL
// （无则 fapi 直连，451 由部署负责）、okx 默认 www.okx.com；binance 代理 base 是 fapi 风格，
// 无 /api/ 段则 fapi→api 得 spot base（与采集一致）。
func resolveExchangeBaseURL(c pgstore.ExchangeConfig) string {
	base := c.BaseURL
	if base == "" {
		switch c.Venue {
		case "binance":
			base = os.Getenv("BINANCE_PROXY_URL")
			if base == "" {
				base = "https://fapi.binance.com" // 无代理直连；451 由部署负责
			}
		case "okx":
			base = "https://www.okx.com" // 实盘域名
		}
	}
	if c.Venue == "binance" && c.VenueType == "spot" && !strings.Contains(base, "/api/") {
		// 与采集一致：仅 spot 需从 fapi 风格代理 base 推导 spot base。
		// naive replace 按首命中换段——仅 spot 走此推导（perp base 含 fapi 段原样保留，
		// 历史 demo 形前缀同样不被错改；D-516 起 sim 行上游 fail-closed 不入此路径）。
		base = strings.Replace(base, "fapi", "api", 1)
	}
	return base
}
