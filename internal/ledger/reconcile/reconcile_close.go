package reconcile

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/data/venue"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
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
	// LEGGED 僵尸收编（超 24h 未恢复）
	listCtx, listCancel = r.withTimeout(ctx)
	leggedPositions, err := r.store.ListPositions(listCtx, "LEGGED", 50, 0)
	listCancel()
	if err != nil {
		log.Printf("[reconcile] list legged positions: %v", err)
	}
	// CLOSING 持仓查询上移（原 3d 段），结果复用给 3d 段 + nonOpenLegKeys 抑制集合。
	listCtx, listCancel = r.withTimeout(ctx)
	closingPositions, err := r.store.ListPositions(listCtx, "CLOSING", 50, 0)
	listCancel()
	if err != nil {
		log.Printf("[reconcile] list closing positions: %v", err)
	}
	now := r.now()

	// localLegs: venue_key+":"+ticket → (leg, position)，收尾需要 position 引用
	// venue 残量按 venue_key+ticket 复合 key 确认（ticket 非全局唯一，跨账户会串仓）。
	type legWithPos struct {
		leg pgstore.PositionLeg
		pos pgstore.Position
	}
	// CEX venue 集合——CEX perp 持仓是聚合仓位（无 ticket/comment），逐单 key
	// 匹配结构性不存在 → CEX 腿不进 localLegs/nonOpenLegKeys，累计 venue+symbol+side 聚合量对拍。
	// 类型断言废（装饰层穿透失效=全瘫根因）——键域解析判 CEX 通道；
	// 腿分类用裸 venue 集（leg.Venue="okx" R1 裸端点，MT 账户号不撞）。
	cexBare := make(map[string]bool)
	for _, q := range r.snapshot() {
		if keydomain.IsCexKey(q.VenueKey()) {
			cexBare[keydomain.BareOf(q.VenueKey())] = true
		}
	}
	cexLocal := make(map[string]float64)    // venueKey:symbol:side → 本地聚合量
	cexLocalPos := make(map[string][]int64) // 同 key → 贡献腿 position_id（告警归因）
	aggLeg := func(leg *pgstore.PositionLeg, posID int64) {
		k := cexAggKey(leg.Venue, leg.Symbol, leg.Side)
		// 返修2（SSOT S1.2 订正）：聚合量用 leg.Qty——venue 单位对拍（qty post-D-311=
		// venue 请求单位，张对张/base 对 base）。勿用 EffectiveQty：其 filled_qty 优先语义对
		// CEX 是基础币，与 venue pos 张数跨单位（498 实证 local=1 XAU vs venue=1000 张必误报）。
		// 边界知悉：pre-D-311 legacy 行 qty=base（当前零活跃 CEX 腿实证）；gate 腿 qty=张 vs
		// venue vol=base（无 gate 持仓，latent）；部分成交时 qty=请求量非实收（tol 吸收或如实告警）。
		cexLocal[k] += leg.Qty
		cexLocalPos[k] = appendPosID(cexLocalPos[k], posID)
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
				if cexBare[keydomain.BareOf(leg.Venue)] {
					aggLeg(&leg, p.ID)
					continue
				}
				key := leg.Venue + ":" + leg.OrderID
				localLegs[key] = legWithPos{leg: leg, pos: p}
			}
		}
	}

	// 2. 查 venue 实际持仓（每个 querier 独立超时，单个失败不阻断其余）
	// venueOrders 也用 venue_key+":"+ticket 复合 key（与 localLegs 对齐）。
	venueOrders := make(map[string]venue.Order) // venue_key+":"+ticket → order
	cexVenue := make(map[string]float64)        // D-385 S1：venueKey:symbol:side → venue 聚合量
	cexReadOK := make(map[string]bool)          // D-401：本轮成功读取的 CEX venueKey（失败读≠零读）
	// venueKey → 该 venue 全部 opened orders（含 CEX；键存在=查询成功=fail-closed 判据）。
	ordersByVenue := make(map[string][]exec.OpenedOrder)
	for _, q := range r.snapshot() {
		sub, cancel := r.withTimeout(ctx)
		orders, err := q.QueryOpenedOrders(sub)
		cancel()
		if err != nil {
			log.Printf("[reconcile] query venue %s: %v（本轮跳过）", q.VenueKey(), err)
			continue
		}
		isCEX := keydomain.IsCexKey(q.VenueKey())
		if isCEX {
			cexReadOK[q.VenueKey()] = true
		}
		// ordersByVenue 全量收集（含 CEX；键存在=本 venue 查询成功，空切片=venue 无单）
		// 喂 zombie 两守卫的 LegAtVenue 匹配——CEX 腿靠 Symbol+Side 而非 ticket。
		ordersByVenue[q.VenueKey()] = ordersByVenue[q.VenueKey()][:0]
		for _, o := range orders {
			ordersByVenue[q.VenueKey()] = append(ordersByVenue[q.VenueKey()], exec.OpenedOrder{
				Ticket: o.Ticket, Symbol: o.Symbol, Side: o.Side, Volume: o.Volume, PosSide: o.PosSide,
			})
			if isCEX {
				cexVenue[cexAggKey(o.VenueKey, o.Symbol, o.Side)] += o.Volume
				continue
			}
			key := o.VenueKey + ":" + o.Ticket
			venueOrders[key] = o
		}
	}

	// nonOpenLegKeys 抑制集合——非 OPEN 态持仓（CLOSING 平仓执行中 / LEGGED 部分成交）
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
				if cexBare[keydomain.BareOf(leg.Venue)] {
					aggLeg(&leg, p.ID) // D-385 S1：非 OPEN 态仓的 CEX 腿同入聚合（真实在仓敞口）
					continue
				}
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

	// 3b/3c. OPENING+LEGGED 僵尸收编（D-073+/D-108 P2 T3；D-417 LegAtVenue 接入后抽公共段）
	mismatches += r.zombieSweep(ctx, openingPositions, leggedPositions, ordersByVenue, now)

	// 3d. D-159 W2a R3-6 / D-189 P0-4：CLOSING 持仓扫描（closingPositions 已在开头查询，D-215 上移复用）。
	// 超时分支调 ResumeClose 自动重触发（不再只告警等人工）；UNKNOWN 只告警不自动重试。
	for _, p := range closingPositions {
		if !isClosingStuck(&p, now) {
			continue
		}
		// 告警按时长分层（<2h critical 每循环；≥2h warn 每小时至多 1 条）——
		// 481 实证：周末休市卡死 ~450 条 critical 淹没真信号；ResumeClose 节奏不变（自愈优先）。
		stuckFor := time.Duration(0)
		if p.ClosingStartedAt != nil {
			stuckFor = now.Sub(*p.ClosingStartedAt)
		}
		// 半平裸奔升级径（warn→critical，阈值 app_config 热读）——
		// 与既有卡死 ResumeClose 重触发并行不替代。
		r.closingNakedCheck(ctx, &p, now)
		// UNKNOWN（uncertainty_reason 非空）只告警，不自动重试（可能已成交，盲平=反向开新仓）。
		if p.UncertaintyReason != nil && *p.UncertaintyReason != "" {
			msg := fmt.Sprintf("CLOSING UNKNOWN position #%d 超 %v 未确认（人工/OrderHistory 查证，不自动重试）", p.ID, closingStuckTimeout)
			r.closingStuckAlert(ctx, &p, stuckFor, msg)
			log.Printf("[reconcile] %s", msg)
			continue
		}
		// 普通 CLOSING 超时 → 自动重触发 ResumeClose。
		msg := fmt.Sprintf("CLOSING position #%d 超 %v 未完成，自动重触发 ResumeClose", p.ID, closingStuckTimeout)
		r.closingStuckAlert(ctx, &p, stuckFor, msg)
		log.Printf("[reconcile] %s", msg)
		if r.closer != nil {
			r.logResumeClose(ctx, &p, stuckFor)
			sub, cancel := r.withTimeout(ctx)
			if err := r.closer.ResumeClose(sub, p.ID); err != nil {
				log.Printf("[reconcile] ResumeClose pos=%d failed (next cycle retry): %v", p.ID, err)
			}
			cancel()
		}
	}

	// venue 有、本地无 → 可能未记录的持仓（D-032：仅 ARB 源订单视为失联；系统外单不追踪）
	// 用复合 key 查 localLegs（venue_key+":"+ticket）。
	for key, vo := range venueOrders {
		if _, ok := localLegs[key]; ok {
			continue // 本地有，正常
		}
		// CLOSING/LEGGED 在途腿在 venue 侧存在是已知状态（正在平/待处置），非失联 → 跳过。
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

	// CEX 聚合对拍（venue+symbol+side 粒度，loop2 之后）；D-401：cexReadOK 供失败读抑制。
	mismatches += r.reconcileCEXAggregate(ctx, cexLocal, cexVenue, cexLocalPos, cexReadOK)

	if mismatches == 0 {
		log.Printf("[reconcile] OK: %d local legs, %d venue orders, %d CEX 聚合键 all matched", len(localLegs), len(venueOrders), len(cexLocal))
	}

	// LEGGED 裸露延迟判定惰性复查（尾部——本 Run 内自愈的也看得到 CLOSED 终态）。
	r.evalLeggedDeferrals(ctx)

	return mismatches, nil
}
