// broker_position_monitor_margin.go —— D-031 ②/R3：MT5 仓位级原生保证金（RequiredMargin
// RPC + 缓存），自 broker_position_monitor.go 拆出（≤300 行契约）。
// 缓存键 = venueKey|ticket|side（R1 退回①：同符号同向两仓 lots 不同，符号键会串值）。
// 失效 = 持仓集签名变化 ∨ 条目龄 >24h（R3：imr 失效路径删除——imr fact 与 RequiredMargin
// 网关值无关，不可达失效路径纯噪声）。RPC 失败记录失败时刻，5 分钟内不重试（R1 退回②
// 退避；退避期内返回 ok=false 诚实空态）。单次 RPC 2s 独立超时（防一 venue 慢卡全部
// 发布——flush 共享 20s ctx）。
package dashboard

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	d031MarginFailBackoff = 5 * time.Minute // RPC 失败退避窗（期内不重试）
	d031MarginRPCTimeout  = 2 * time.Second // 单次 RequiredMargin 独立超时
	d031MarginEntryTTL    = 24 * time.Hour  // 条目龄上限（超龄失效重拉）
)

// d031MarginEntry 保证金缓存条目（值 + 拉取时刻——24h TTL 失效）。
type d031MarginEntry struct {
	val float64
	at  time.Time
}

// d031MarginState D-031 保证金域状态（BrokerPositionMonitor 内嵌字段）。
type d031MarginState struct {
	reqMarginFor func(ctx context.Context, venueKey, symbol, side string, lots, price float64) (float64, bool)
	cache        map[string]d031MarginEntry // venueKey|ticket|side → 值
	lastPairSig  map[string]string          // venueKey → 持仓集签名
	failAt       map[string]time.Time       // 缓存键 → 上次 RPC 失败时刻
	mu           sync.Mutex
}

// SetRequiredMarginFor D-031 ②：注入仓位级原生保证金 RPC 口（BrokerManager.RequiredMarginFor
// 包装；nil=未注入→MT5 保证金列诚实空态）。
func (m *BrokerPositionMonitor) SetRequiredMarginFor(fn func(ctx context.Context, venueKey, symbol, side string, lots, price float64) (float64, bool)) {
	m.d031.mu.Lock()
	defer m.d031.mu.Unlock()
	m.d031.reqMarginFor = fn
}

// cachedRequiredMargin D-031 ②/R3：仓位级保证金（带缓存——禁每帧 RPC）。
// 键=venueKey|ticket|side（R1 退回①）。失效=持仓集签名变化 ∨ 条目龄 >24h。
// RPC 失败记失败时刻 5min 退避（期内 ok=false 不重试）。
func (m *BrokerPositionMonitor) cachedRequiredMargin(ctx context.Context, vk, venueKey, symbol, canonical, side, ticket string, lots, openPrice float64) (float64, bool) {
	if m.d031.reqMarginFor == nil {
		return 0, false
	}
	key := venueKey + "|" + ticket + "|" + side
	sig := m.positionSetSig(vk)
	now := time.Now()

	m.d031.mu.Lock()
	if m.d031.lastPairSig == nil {
		m.d031.lastPairSig = map[string]string{}
		m.d031.cache = map[string]d031MarginEntry{}
		m.d031.failAt = map[string]time.Time{}
	}
	sigChanged := m.d031.lastPairSig[vk] != sig
	if sigChanged {
		m.d031.cache = map[string]d031MarginEntry{}
		m.d031.lastPairSig[vk] = sig
	}
	ent, cached := m.d031.cache[key]
	m.d031.mu.Unlock()

	if cached && now.Sub(ent.at) > d031MarginEntryTTL {
		cached = false // 条目龄 >24h → 失效重拉
	}
	if cached {
		return ent.val, true
	}
	// R1 退回②：RPC 失败 5min 退避（期内 ok=false 不重试）。
	if m.d031failPending(key) {
		return 0, false
	}
	// 单次 RPC 2s 独立超时（flush 共享 20s ctx，防一 venue 慢卡全部发布）。
	rctx, cancel := context.WithTimeout(ctx, d031MarginRPCTimeout)
	defer cancel()
	val, ok := m.d031.reqMarginFor(rctx, venueKey, symbol, side, lots, openPrice)
	if !ok {
		m.d031failMark(key)
		return 0, false
	}
	m.d031cachePut(key, d031MarginEntry{val: val, at: now})
	return val, true
}

// d031cachePut / d031failMark / d031failPending 域小件（mu 收口）。
func (m *BrokerPositionMonitor) d031cachePut(key string, e d031MarginEntry) {
	m.d031.mu.Lock()
	defer m.d031.mu.Unlock()
	if m.d031.cache == nil {
		m.d031.cache = map[string]d031MarginEntry{}
	}
	m.d031.cache[key] = e
}

func (m *BrokerPositionMonitor) d031failMark(key string) {
	m.d031.mu.Lock()
	defer m.d031.mu.Unlock()
	if m.d031.failAt == nil {
		m.d031.failAt = map[string]time.Time{}
	}
	m.d031.failAt[key] = time.Now()
}

func (m *BrokerPositionMonitor) d031failPending(key string) bool {
	m.d031.mu.Lock()
	defer m.d031.mu.Unlock()
	at, has := m.d031.failAt[key]
	return has && time.Since(at) < d031MarginFailBackoff
}

// positionSetSig 持仓集签名（venueState.orders 键集排序串）。
func (m *BrokerPositionMonitor) positionSetSig(vk string) string {
	vs, ok := m.venues[vk]
	if !ok {
		return ""
	}
	keys := make([]string, 0, len(vs.orders))
	for k := range vs.orders {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
