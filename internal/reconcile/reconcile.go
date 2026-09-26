// Package reconcile 实现持仓对账（§13.4 B2）。
// 查 venue 实际持仓与本地 OPEN legs 比对，不一致 → critical 告警。
package reconcile

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Reconciler 对账器：比对本地 OPEN legs 与 venue 实际持仓。
type Reconciler struct {
	store Store
	mu    sync.RWMutex
	// D-245：CLOSING 告警分层的小时桶（posID→上次 warn 的整小时），进程重启最多重复 1 条（不落库）。
	closingAlertHour map[int64]int64
	// D-401：CEX 聚合对拍连续确认计数（agg key→连续不一致轮数，达 3 才 critical）。
	// 仅 Run 路径串行读写，无锁；runTimeout 超时/查询失败的轮次冻结不计数。
	cexAggPending map[string]int
	// D-417 §1.4：CEX 系统外单 warn 同指纹冷却（key→上次告警时刻，24h 单发）。
	cexForeignWarned map[string]time.Time
	// D-407 S1/S2：残腿密集重试（fast retry）。window=CLOSING 起算的密集重试窗口
	// （窗外静默交还 30min 看护）；consecN=同 venue 连续失败升级阈；
	// streak/times=venue 维度失败计数（内存态 v1，重启清零可接受——告警文本如实）。
	fastRetryWindow time.Duration
	fastRetryConsec int
	fastRetryStreak map[string]int
	fastRetryTimes  map[string][]time.Time
	// D-245：可注入时钟（测试假时钟；nil=time.Now）。
	nowFn           func() time.Time
	queriers        []VenueQuerier
	historyQueriers []OrderHistoryQuerier
	// D-421 §1.5：CEX 订单级查单器（消解器+收编前置守卫；venueKey→querier）。
	orderStatusQs map[string]OrderStatusQuerier
	// D-421：消解不可判轮次计数（posID 粒度，消解/人工后清）。
	unknownRounds map[int64]int
	closer        Closer
	settler       Settler       // D-087 §3.2：替换 statUpdater + verificationUpdater（统一结算入口）
	runTimeout    time.Duration // 单次外部调用超时（缺陷1修复：默认 10s，防离线 venue 阻塞 loop）
	initialDelay  time.Duration // 首轮延迟（缺陷2修复：默认 60s，等 adapter 就绪）
}

// New 创建对账器。
func New(store Store, queriers ...VenueQuerier) *Reconciler {
	return &Reconciler{
		store:            store,
		queriers:         queriers,
		runTimeout:       10 * time.Second,
		initialDelay:     60 * time.Second,
		closingAlertHour: map[int64]int64{},
		cexAggPending:    map[string]int{},
		cexForeignWarned: map[string]time.Time{},
		nowFn:            time.Now,
		fastRetryWindow:  10 * time.Minute,
		fastRetryConsec:  3,
		fastRetryStreak:  map[string]int{},
		fastRetryTimes:   map[string][]time.Time{},
		unknownRounds:    map[int64]int{},
	}
}

// SetFastRetryParams D-407 S1：注入密集重试参数（cmd 层 env → 本 setter；≤0 保持默认）。
func (r *Reconciler) SetFastRetryParams(window time.Duration, consecN int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if window > 0 {
		r.fastRetryWindow = window
	}
	if consecN > 0 {
		r.fastRetryConsec = consecN
	}
}

// now D-245：可注入时钟（nil=time.Now）。
func (r *Reconciler) now() time.Time {
	if r.nowFn == nil {
		return time.Now()
	}
	return r.nowFn()
}

// SetCloser 注入平仓器（对账自动收尾用，D-073+）。main 装配 service 后调。
func (r *Reconciler) SetCloser(c Closer) { r.closer = c }

// SetSettler 注入结算器（D-087 §3.2，替换 SetStatUpdater + SetVerificationUpdater）。
func (r *Reconciler) SetSettler(s Settler) { r.settler = s }

// SetOrderHistoryQueriers 设置历史单查证器集合（与 VenueQuerier 同 venueKey 身份）。
func (r *Reconciler) SetOrderHistoryQueriers(qs ...OrderHistoryQuerier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.historyQueriers = append([]OrderHistoryQuerier(nil), qs...)
}

// SetQueriers 整体替换 querier 集合（Broker 管理器启停适配器时调用）。
func (r *Reconciler) SetQueriers(qs ...VenueQuerier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queriers = append([]VenueQuerier(nil), qs...)
}

// snapshot 返回 querier 集合的快照（Run 期间避免对集合的并发读写）。
func (r *Reconciler) snapshot() []VenueQuerier {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.queriers
}

// historySnapshot 返回历史单查证器集合快照。
func (r *Reconciler) historySnapshot() []OrderHistoryQuerier {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.historyQueriers
}

// findHistoryQuerier 按 venueKey 找历史单查证器（venueKey = "{venue}:{type}"，与 leg.Venue 同源）。
func (r *Reconciler) findHistoryQuerier(venueKey string) OrderHistoryQuerier {
	for _, q := range r.historySnapshot() {
		if q.VenueKey() == venueKey {
			return q
		}
	}
	return nil
}

// HasHistoryQuerier D-422：该 venue 是否有已注册历史查证器（含 D-416 DB 账户动态注册）。
func (r *Reconciler) HasHistoryQuerier(venueKey string) bool {
	return r.findHistoryQuerier(venueKey) != nil
}

// OpenedByVenueKey 按 venueKey 查该 venue 在仓订单（修复⑫：exec G1 回读门面）。
// 未注册该 venue（含 CEX）→ error（调用方 fail-open 跳过）。锁语义对齐 snapshot()。
func (r *Reconciler) OpenedByVenueKey(ctx context.Context, venueKey string) ([]VenueOrder, error) {
	for _, q := range r.snapshot() {
		if q.VenueKey() == venueKey {
			return q.QueryOpenedOrders(ctx)
		}
	}
	return nil, fmt.Errorf("reconcile: no querier for venueKey %s", venueKey)
}

// HistoryByVenueKey 按 venueKey+ticket 查历史单（修复⑫：exec M1 回读校准门面）。
// 未注册该 venue（含 CEX）或未命中 → (*HistoryOrder=nil, nil)；error 仅查询失败。
func (r *Reconciler) HistoryByVenueKey(ctx context.Context, venueKey, ticket string, fromDays int) (*HistoryOrder, error) {
	q := r.findHistoryQuerier(venueKey)
	if q == nil {
		return nil, nil
	}
	return q.QueryOrderHistory(ctx, ticket, fromDays)
}

// withTimeout 派生带超时的 ctx（缺陷1修复：所有外部调用一律包超时，防离线 venue 阻塞 loop）。
func (r *Reconciler) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, r.runTimeout)
}

// insertAlertCtx 带超时插告警（缺陷1修复：InsertAlert 也包超时）。
func (r *Reconciler) insertAlertCtx(ctx context.Context, level, msg string) {
	sub, cancel := r.withTimeout(ctx)
	defer cancel()
	if err := r.store.InsertAlert(sub, nil, level, msg); err != nil {
		log.Printf("[reconcile] alert: %v", err)
	}
}
