// Package reconcile 实现持仓对账（§13.4 B2）。
// 查 venue 实际持仓与本地 OPEN legs 比对，不一致 → critical 告警。
package reconcile

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"arb/internal/store/pgstore"
)

// Reconciler 对账器：比对本地 OPEN legs 与 venue 实际持仓。
type Reconciler struct {
	store           Store
	mu              sync.RWMutex
	queriers        []VenueQuerier
	historyQueriers []OrderHistoryQuerier
	closer          Closer
	settler         Settler       // D-087 §3.2：替换 statUpdater + verificationUpdater（统一结算入口）
	runTimeout      time.Duration // 单次外部调用超时（缺陷1修复：默认 10s，防离线 venue 阻塞 loop）
	initialDelay    time.Duration // 首轮延迟（缺陷2修复：默认 60s，等 adapter 就绪）
}

// New 创建对账器。
func New(store Store, queriers ...VenueQuerier) *Reconciler {
	return &Reconciler{
		store:        store,
		queriers:     queriers,
		runTimeout:   10 * time.Second,
		initialDelay: 60 * time.Second,
	}
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

// Run 执行一次对账。返回不一致数量。
// 缺陷1修复：所有外部调用（venue querier / OrderHistory / InsertAlert / store 写）一律包 runTimeout 超时，
// 单个 venue 超时不炸整个 Run（该 venue 本轮跳过 + 计数日志）。
// 缺陷2修复：findHistoryQuerier 返回 nil 时不告警不计数（查证器未就绪，下轮复查），仅 debug 日志。
//
// S5 告警文案 token 格式是前端解析契约（改动须同步 web/src/format.ts parseAlertTarget）：
//   - 「本地有 venue 无」/收尾类告警：含 #<position_id>（前端 #\d+ 解析 → 跳持仓页）
//   - 「venue 有 本地无」孤儿单告警：含 ticket=NNN（前端 ticket=\d+ 解析 → 跳 Broker 页）
func (r *Reconciler) Run(ctx context.Context) (int, error) {
	// 1. 查本地 OPEN + OPENING 持仓（OPENING 僵尸收编，D-073+）
	listCtx, listCancel := r.withTimeout(ctx)
	openPositions, err := r.store.ListPositions(listCtx, "OPEN", 200, 0)
	listCancel()
	if err != nil {
		return 0, fmt.Errorf("list open positions: %w", err)
	}
	listCtx, listCancel = r.withTimeout(ctx)
	openingPositions, err := r.store.ListPositions(listCtx, "OPENING", 50, 0)
	listCancel()
	if err != nil {
		log.Printf("[reconcile] list opening positions: %v", err)
	}
	// D-108 P2 T3：LEGGED 僵尸收编（超 24h 未恢复）
	listCtx, listCancel = r.withTimeout(ctx)
	leggedPositions, err := r.store.ListPositions(listCtx, "LEGGED", 50, 0)
	listCancel()
	if err != nil {
		log.Printf("[reconcile] list legged positions: %v", err)
	}
	now := time.Now()

	// localLegs: ticket → (leg, position)，收尾需要 position 引用
	type legWithPos struct {
		leg pgstore.PositionLeg
		pos pgstore.Position
	}
	localLegs := make(map[string]legWithPos)
	for _, p := range openPositions {
		sub, cancel := r.withTimeout(ctx)
		legs, err := r.store.ListPositionLegs(sub, p.ID)
		cancel()
		if err != nil {
			log.Printf("[reconcile] list legs pos %d: %v", p.ID, err)
			continue
		}
		for _, leg := range legs {
			if leg.OrderID != "" && leg.Phase == "OPEN" {
				localLegs[leg.OrderID] = legWithPos{leg: leg, pos: p}
			}
		}
	}

	// 2. 查 venue 实际持仓（每个 querier 独立超时，单个失败不阻断其余）
	venueOrders := make(map[string]VenueOrder) // ticket → order
	for _, q := range r.snapshot() {
		sub, cancel := r.withTimeout(ctx)
		orders, err := q.QueryOpenedOrders(sub)
		cancel()
		if err != nil {
			log.Printf("[reconcile] query venue %s: %v（本轮跳过）", q.VenueKey(), err)
			continue
		}
		for _, o := range orders {
			venueOrders[o.Ticket] = o
		}
	}

	// 3. 比对
	mismatches := 0

	// 本地有、venue 无 → 查证 OrderHistory → 命中收尾 / 未命中 critical（D-073+）
	for ticket, lp := range localLegs {
		if _, ok := venueOrders[ticket]; ok {
			continue // venue 有，正常
		}
		// 查证：调该 venue OrderHistory(ticket)
		hq := r.findHistoryQuerier(lp.leg.Venue)
		if hq == nil {
			// 缺陷2修复：查证器未就绪（CEX 无查证器 / adapter 未连）→ 不告警不计数，下轮复查
			log.Printf("[reconcile] 查证器未就绪 ticket=%s venue=%s，下轮复查", ticket, lp.leg.Venue)
			continue
		}
		sub, cancel := r.withTimeout(ctx)
		hist, err := hq.QueryOrderHistory(sub, ticket, 7) // 查近 7 天
		cancel()
		if err != nil {
			// 查证失败 = fail-closed（不动仓位 + critical 带错误）
			mismatches++
			msg := fmt.Sprintf("对账查证失败 position #%d ticket=%s venue=%s: %v（人工处置）", lp.pos.ID, ticket, lp.leg.Venue, err)
			r.insertAlertCtx(ctx, "critical", msg)
			log.Printf("[reconcile] %s", msg)
			continue
		}
		if hist == nil {
			// 未命中（历史也无）→ 维持 critical（真失联）
			mismatches++
			msg := fmt.Sprintf("对账不一致：本地 leg ticket=%s venue=%s symbol=%s 在 venue 侧未找到（历史单也无，真失联）",
				ticket, lp.leg.Venue, lp.leg.Symbol)
			r.insertAlertCtx(ctx, "critical", msg)
			log.Printf("[reconcile] %s", msg)
			continue
		}
		// 命中（已平仓）→ 自动收尾流水
		if err := r.autoClose(ctx, &lp.pos, lp.leg, hist); err != nil {
			// 收尾失败 = fail-closed（critical 带原因，下轮重试）
			mismatches++
			msg := fmt.Sprintf("对账自动收尾失败 position #%d ticket=%s: %v（下轮重试）", lp.pos.ID, ticket, err)
			r.insertAlertCtx(ctx, "critical", msg)
			log.Printf("[reconcile] %s", msg)
			continue
		}
		// 收尾成功（warn 已在 autoClose 内插，不计 mismatches）
		log.Printf("[reconcile] auto-close completed pos=%d ticket=%s", lp.pos.ID, ticket)
	}

	// 3b. OPENING 僵尸收编（超 24h 或无 opened_at 且无 OPENING 腿，D-073+ 补充条件）
	for _, p := range openingPositions {
		legsCtx, legsCancel := r.withTimeout(ctx)
		legs, err := r.store.ListPositionLegs(legsCtx, p.ID)
		legsCancel()
		if err != nil {
			log.Printf("[reconcile] list legs for OPENING zombie pos=%d failed: %v (skip this round)", p.ID, err)
			continue
		}
		if !isOPENINGZombie(&p, legs, now) {
			continue
		}
		if err := r.autoFailOPENINGZombie(ctx, &p, venueOrders); err != nil {
			mismatches++
			msg := fmt.Sprintf("OPENING 僵尸归档失败 position #%d: %v", p.ID, err)
			r.insertAlertCtx(ctx, "critical", msg)
			log.Printf("[reconcile] %s", msg)
		}
	}

	// 3c. LEGGED 僵尸收编（超 24h 未恢复，D-108 P2 T3）
	for _, p := range leggedPositions {
		if !isLeggedZombie(&p, now) {
			continue
		}
		if err := r.autoFailLeggedZombie(ctx, &p, venueOrders); err != nil {
			mismatches++
			msg := fmt.Sprintf("LEGGED 僵尸归档失败 position #%d: %v", p.ID, err)
			r.insertAlertCtx(ctx, "critical", msg)
			log.Printf("[reconcile] %s", msg)
		}
	}

	// venue 有、本地无 → 可能未记录的持仓（D-032：仅 ARB 源订单视为失联；系统外单不追踪）
	for ticket, vo := range venueOrders {
		if _, ok := localLegs[ticket]; ok {
			continue // 本地有，正常
		}
		if !isARBOrder(vo.Comment) {
			log.Printf("[reconcile] venue %s 持仓 ticket=%s symbol=%s comment=%q 为系统外单，不追踪",
				vo.Venue, ticket, vo.Symbol, vo.Comment)
			continue
		}
		mismatches++
		msg := fmt.Sprintf("对账不一致：venue %s 持仓 ticket=%s symbol=%s side=%s vol=%f 本地未记录（ARB 源订单失联）",
			vo.Venue, ticket, vo.Symbol, vo.Side, vo.Volume)
		r.insertAlertCtx(ctx, "critical", msg)
		log.Printf("[reconcile] %s", msg)
	}

	if mismatches == 0 {
		log.Printf("[reconcile] OK: %d local legs, %d venue orders, all matched", len(localLegs), len(venueOrders))
	}

	return mismatches, nil
}
