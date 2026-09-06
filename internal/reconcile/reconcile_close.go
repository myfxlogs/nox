package reconcile

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/store/pgstore"
)

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
	// D-215：CLOSING 持仓查询上移（原 3d 段），结果复用给 3d 段 + nonOpenLegKeys 抑制集合。
	listCtx, listCancel = r.withTimeout(ctx)
	closingPositions, err := r.store.ListPositions(listCtx, "CLOSING", 50, 0)
	listCancel()
	if err != nil {
		log.Printf("[reconcile] list closing positions: %v", err)
	}
	now := r.now()

	// localLegs: venue_key+":"+ticket → (leg, position)，收尾需要 position 引用
	// D-159 W2a R3-7：venue 残量按 venue_key+ticket 复合 key 确认（ticket 非全局唯一，跨账户会串仓）。
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
				key := leg.Venue + ":" + leg.OrderID
				localLegs[key] = legWithPos{leg: leg, pos: p}
			}
		}
	}

	// 2. 查 venue 实际持仓（每个 querier 独立超时，单个失败不阻断其余）
	// D-159 W2a R3-7：venueOrders 也用 venue_key+":"+ticket 复合 key（与 localLegs 对齐）。
	venueOrders := make(map[string]VenueOrder) // venue_key+":"+ticket → order
	for _, q := range r.snapshot() {
		sub, cancel := r.withTimeout(ctx)
		orders, err := q.QueryOpenedOrders(sub)
		cancel()
		if err != nil {
			log.Printf("[reconcile] query venue %s: %v（本轮跳过）", q.VenueKey(), err)
			continue
		}
		for _, o := range orders {
			key := o.VenueKey + ":" + o.Ticket
			venueOrders[key] = o
		}
	}

	// D-215：nonOpenLegKeys 抑制集合——非 OPEN 态持仓（CLOSING 平仓执行中 / LEGGED 部分成交）
	// 的在途腿 key。这些腿在 venue 侧存在是已知状态（正在平/待处置），不是失联——孤儿检查跳过。
	// 不并入 localLegs：并入会走「本地有 venue 无→查证→autoClose」路径，autoClose 对 CLOSING 的
	// ClaimClose 必返 ErrClaimFailed → 每 5min 死循环（设计已验证，勿改）。
	nonOpenLegKeys := make(map[string]int64) // venue_key+":"+ticket → position_id（日志用）
	nonOpenPositions := make([]pgstore.Position, 0, len(closingPositions)+len(leggedPositions))
	nonOpenPositions = append(nonOpenPositions, closingPositions...)
	nonOpenPositions = append(nonOpenPositions, leggedPositions...)
	for _, p := range nonOpenPositions {
		sub, cancel := r.withTimeout(ctx)
		legs, err := r.store.ListPositionLegs(sub, p.ID)
		cancel()
		if err != nil {
			log.Printf("[reconcile] list legs for non-OPEN pos %d: %v (skip suppression)", p.ID, err)
			continue
		}
		for _, leg := range legs {
			if leg.OrderID != "" && leg.Phase == "OPEN" {
				nonOpenLegKeys[leg.Venue+":"+leg.OrderID] = p.ID
			}
		}
	}

	// 3. 比对
	mismatches := 0

	// 本地有、venue 无 → 查证 OrderHistory → 命中收尾 / 未命中 critical（D-073+）
	for key, lp := range localLegs {
		if _, ok := venueOrders[key]; ok {
			continue // venue 有，正常
		}
		// 查证用原 ticket（hq.QueryOrderHistory 按 ticket 查，venue 由 hq 隐含）
		ticket := lp.leg.OrderID
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

	// 3d. D-159 W2a R3-6 / D-189 P0-4：CLOSING 持仓扫描（closingPositions 已在开头查询，D-215 上移复用）。
	// D-189 P0-4：超时分支调 ResumeClose 自动重触发（不再只告警等人工）；UNKNOWN 只告警不自动重试。
	for _, p := range closingPositions {
		if !isClosingStuck(&p, now) {
			continue
		}
		// D-245：告警按时长分层（<2h critical 每循环；≥2h warn 每小时至多 1 条）——
		// 481 实证：周末休市卡死 ~450 条 critical 淹没真信号；ResumeClose 节奏不变（自愈优先）。
		stuckFor := time.Duration(0)
		if p.ClosingStartedAt != nil {
			stuckFor = now.Sub(*p.ClosingStartedAt)
		}
		// D-189 P0-4 S2：UNKNOWN（uncertainty_reason 非空）只告警，不自动重试（可能已成交，盲平=反向开新仓）。
		if p.UncertaintyReason != nil && *p.UncertaintyReason != "" {
			msg := fmt.Sprintf("CLOSING UNKNOWN position #%d 超 %v 未确认（人工/OrderHistory 查证，不自动重试）", p.ID, closingStuckTimeout)
			r.closingStuckAlert(ctx, &p, stuckFor, msg)
			log.Printf("[reconcile] %s", msg)
			continue
		}
		// D-189 P0-4 S5：普通 CLOSING 超时 → 自动重触发 ResumeClose。
		msg := fmt.Sprintf("CLOSING position #%d 超 %v 未完成，自动重触发 ResumeClose", p.ID, closingStuckTimeout)
		r.closingStuckAlert(ctx, &p, stuckFor, msg)
		log.Printf("[reconcile] %s", msg)
		if r.closer != nil {
			sub, cancel := r.withTimeout(ctx)
			if err := r.closer.ResumeClose(sub, p.ID); err != nil {
				log.Printf("[reconcile] ResumeClose pos=%d failed (next cycle retry): %v", p.ID, err)
			}
			cancel()
		}
	}

	// venue 有、本地无 → 可能未记录的持仓（D-032：仅 ARB 源订单视为失联；系统外单不追踪）
	// D-159 W2a R3-7：用复合 key 查 localLegs（venue_key+":"+ticket）。
	for key, vo := range venueOrders {
		if _, ok := localLegs[key]; ok {
			continue // 本地有，正常
		}
		// D-215：CLOSING/LEGGED 在途腿在 venue 侧存在是已知状态（正在平/待处置），非失联 → 跳过。
		if posID, ok := nonOpenLegKeys[key]; ok {
			log.Printf("[reconcile] venue %s ticket=%s 属 CLOSING/LEGGED position #%d 在途腿，跳过失联告警", vo.Venue, vo.Ticket, posID)
			continue
		}
		if !isARBOrder(vo.Comment) {
			log.Printf("[reconcile] venue %s 持仓 ticket=%s symbol=%s comment=%q 为系统外单，不追踪",
				vo.Venue, vo.Ticket, vo.Symbol, vo.Comment)
			continue
		}
		mismatches++
		msg := fmt.Sprintf("对账不一致：venue %s 持仓 ticket=%s symbol=%s side=%s vol=%f 本地未记录（ARB 源订单失联）",
			vo.Venue, vo.Ticket, vo.Symbol, vo.Side, vo.Volume)
		r.insertAlertCtx(ctx, "critical", msg)
		log.Printf("[reconcile] %s", msg)
	}

	if mismatches == 0 {
		log.Printf("[reconcile] OK: %d local legs, %d venue orders, all matched", len(localLegs), len(venueOrders))
	}

	return mismatches, nil
}

// closingStuckTimeout D-159 W2a R3-6：CLOSING 超 30min 未完成=卡住（平仓 I/O 失败或残量未归零）。
const closingStuckTimeout = 30 * time.Minute

// isClosingStuck D-159 W2a R3-6 / D-189 P0-4：判断 CLOSING 持仓是否卡住（超时未完成）。
// D-189 P0-4：改用 ClosingStartedAt（进入 CLOSING 的真实起始时间），不再用 opened_at（开仓时间）。
// 无 ClosingStartedAt = 异常（直接判卡住，fail-closed）；迁移已为存量 CLOSING 行补当前时间。

// closingStuckAlert D-245：CLOSING 看护告警分层——卡死 <2h 维持 critical 每循环；
// ≥2h 降为 warn 且每小时至多 1 条（closingAlertHour 小时桶，进程重启最多重复 1 条）。
// ResumeClose 重试节奏不受影响（降的是告警不是重试）。
func (r *Reconciler) closingStuckAlert(ctx context.Context, p *pgstore.Position, stuckFor time.Duration, msg string) {
	if p.ClosingStartedAt == nil || stuckFor < closingTierWarnAfter {
		r.insertAlertCtx(ctx, "critical", msg)
		return
	}
	hour := r.now().Unix() / 3600
	r.mu.RLock()
	last, ok := r.closingAlertHour[p.ID]
	r.mu.RUnlock()
	if ok && last == hour {
		return
	}
	r.mu.Lock()
	r.closingAlertHour[p.ID] = hour
	r.mu.Unlock()
	r.insertAlertCtx(ctx, "warn", msg)
}

// closingTierWarnAfter D-245：warn 分层门槛（卡死 ≥2h 降频）。
const closingTierWarnAfter = 2 * time.Hour

func isClosingStuck(pos *pgstore.Position, now time.Time) bool {
	if pos.Status != "CLOSING" {
		return false
	}
	if pos.ClosingStartedAt == nil {
		return true // 无 closing_started_at=异常（fail-closed）
	}
	return now.Sub(*pos.ClosingStartedAt) > closingStuckTimeout
}
