// broker_position_monitor_margin.go —— D-031 ②/R2：MT5 仓位级原生保证金（RequiredMargin
// RPC + 缓存），自 broker_position_monitor.go 拆出（≤300 行契约）。
// 缓存键 = venueKey|ticket|side（R1 退回①：同符号同向两仓 lots 不同，符号键会串值）。
// 失效 = 持仓集签名变化 ∨ init_margin_rate fact 值变化（imr 读口仅在缓存 miss/签名
// 变化路径执行——R1 退回③：命中路径禁每帧查库）。RPC 失败记录失败时刻，5 分钟内
// 不重试（R1 退回②退避；退避期内返回 ok=false 诚实空态）。单次 RPC 2s 独立超时
// （退回补记：防一 venue 慢卡全部发布——flush 共享 20s ctx）。
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
)

// d031MarginEntry 保证金缓存条目（值 + 拉取时 init_margin_rate 快照——imr 变化失效）。
type d031MarginEntry struct {
	val float64
	imr float64
}

// d031MarginState D-031 保证金域状态（BrokerPositionMonitor 内嵌字段）。
type d031MarginState struct {
	reqMarginFor func(ctx context.Context, venueKey, symbol, side string, lots, price float64) (float64, bool)
	imrFor       func(ctx context.Context, venue, canonical string) (float64, bool)
	cache        map[string]d031MarginEntry // venueKey|ticket|side → 值
	imr          map[string]float64         // venueKey → 最近 imr fact 值
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

// SetInitMarginRateFn D-031 ②：注入 init_margin_rate fact 读口（缓存失效信号之二：
// 值变化→缓存失效重拉；nil=仅持仓集签名失效）。
func (m *BrokerPositionMonitor) SetInitMarginRateFn(fn func(ctx context.Context, venue, canonical string) (float64, bool)) {
	m.d031.imrFor = fn
}

// cachedRequiredMargin D-031 ②：仓位级保证金（带缓存——禁每帧 RPC）。
// 键=venueKey|ticket|side（R1 退回①）。失效=持仓集签名变化 ∨ init_margin_rate fact 值变化
// （imr 读口仅 miss/签名变化路径执行——R1 退回③）。RPC 失败记失败时刻 5min 退避。
func (m *BrokerPositionMonitor) cachedRequiredMargin(ctx context.Context, vk, venueKey, symbol, canonical, side, ticket string, lots, openPrice float64) (float64, bool) {
	if m.d031.reqMarginFor == nil {
		return 0, false
	}
	key := venueKey + "|" + ticket + "|" + side
	sig := m.positionSetSig(vk)

	m.d031.mu.Lock()
	if m.d031.lastPairSig == nil {
		m.d031.lastPairSig = map[string]string{}
		m.d031.cache = map[string]d031MarginEntry{}
		m.d031.imr = map[string]float64{}
		m.d031.failAt = map[string]time.Time{}
	}
	sigChanged := m.d031.lastPairSig[vk] != sig
	if sigChanged {
		m.d031.cache = map[string]d031MarginEntry{}
		m.d031.lastPairSig[vk] = sig
	}
	ent, cached := m.d031.cache[key]
	m.d031.mu.Unlock()

	// R1 退回③：imr 读口只在缓存 miss/签名变化路径执行——命中路径零查库。
	needFetch := !cached || sigChanged
	var imr float64
	imrKnown := false
	if needFetch && m.d031.imrFor != nil {
		imr, imrKnown = m.d031.imrFor(ctx, venueKey, canonical)
	}
	if cached && imrKnown && ent.imr != imr {
		needFetch = true // imr 变化 → 缓存失效重拉
	}
	if !needFetch && cached {
		return ent.val, true
	}
	// R1 退回②：RPC 失败 5min 退避（期内 ok=false 不重试）。
	if m.d031failPending(key) {
		return 0, false
	}
	// R2 补记：单次 RPC 2s 独立超时（flush 共享 20s ctx，防一 venue 慢卡全部发布）。
	rctx, cancel := context.WithTimeout(ctx, d031MarginRPCTimeout)
	defer cancel()
	val, ok := m.d031.reqMarginFor(rctx, venueKey, symbol, side, lots, openPrice)
	if !ok {
		m.d031failMark(key)
		return 0, false
	}
	if imrKnown {
		m.d031cachePut(key, d031MarginEntry{val: val, imr: imr})
	} else {
		m.d031cachePut(key, d031MarginEntry{val: val})
	}
	return val, true
}

// d031cacheGet / d031cachePut / d031failMark / d031failPending 域小件（mu 收口）。
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
