// engine_exit_events.go —— D-004 P1#4：退出评估分层。
// 分事件源即到即评：quote 帧走纯内存快路（quoteExitHit 命中才升权威评估），
// 订单事件/carry facts/到点边界走权威全量 scanAndClose；fallback ticker 退化
// 为饥荒看门狗（只告警不评估）。30s 节流丢弃语义废除。
package autotrade

import (
	"context"
	"log"
	"time"

	"nox/internal/risk/position"
	"nox/internal/store/pgstore"
)

// authMinInterval 同一仓 quote 快路命中态持续时（如休市 suspend）升权威评估的合流
// 间隔——不是轮询，命中才计时。
const authMinInterval = time.Second

// kickCoalesce 订单事件 kick 合流窗口：事件不丢，窗口内补跑一次。
const kickCoalesce = time.Second

// openEntry positionLoop 内存持仓缓存行（单协程读写，同 lastScan 先例）。
// lastAuth=该仓最近一次权威评估时刻（quote 命中合流用）。
type openEntry struct {
	pos       pgstore.Position
	legs      []pgstore.PositionLeg
	canonical string
	// sellCanonical 卖腿 canonical 重载期解析缓存（xassetSellCanonical 含 DB——
	// quote 快路逐帧禁查，改在 reloadOpen 一次性解析）。
	sellCanonical string
	lastAuth      time.Time
}

// reloadOpen 重载内存持仓缓存（boot/订单事件后/全量评估后调用）。
// ListPositions 出错 → 保留 prev（陈旧缓存优于失明：返回 nil 会让 quote 快路
// 全盲且看门狗 len(open)>0 不成立=静默）；单仓 legs 失败仅跳过该仓。
func (e *Engine) reloadOpen(ctx context.Context, prev []openEntry) []openEntry {
	positions, err := e.store.ListPositions(ctx, "OPEN", 50, 0)
	if err != nil {
		log.Printf("[autotrade] reloadOpen list positions: %v (keep stale cache %d)", err, len(prev))
		return prev
	}
	open := make([]openEntry, 0, len(positions))
	for _, p := range positions {
		legs, err := e.store.ListPositionLegs(ctx, p.ID)
		if err != nil {
			log.Printf("[autotrade] reloadOpen legs pos=%d: %v", p.ID, err)
			continue // 单仓失败不阻塞其余；权威路径逐仓自理
		}
		open = append(open, openEntry{
			pos: p, legs: legs, canonical: canonicalFromOppID(p.OpportunityID),
			sellCanonical: e.xassetSellCanonical(ctx, &p, legs), lastAuth: time.Now(),
		})
	}
	return open
}

// quoteExitHit quote 帧纯内存退出命中判定（零 DB/RPC；legs 用缓存）。
// CEX/非息差型=收敛判据；SWAPARB/CARRY=基差止损；其余息差型非 quote 驱动恒 false。
func (e *Engine) quoteExitHit(en *openEntry) bool {
	p := &en.pos
	if !position.IsCarryType(p.Type) {
		netBps, ok := e.currentNetBpsLegs(p, en.legs)
		return ok && netBps <= e.cfg.CloseBpsThr
	}
	if p.Type == "SWAPARB" || p.Type == "CARRY" {
		ok, _ := e.basisStopCheckResolved(p, en.legs, en.sellCanonical)
		return ok
	}
	return false
}

// nextExitBoundary 时间驱动退出边界：probe 到期、PERP_FUNDING 持有下限。
// 取最早未来时刻；无边界 → ok=false（调用方停表）。
func (e *Engine) nextExitBoundary(open []openEntry) (time.Time, bool) {
	var earliest time.Time
	found := false
	push := func(t time.Time) {
		if t.After(time.Now()) && (!found || t.Before(earliest)) {
			earliest, found = t, true
		}
	}
	for i := range open {
		if d, ok := e.probeDeadline(&open[i].pos); ok {
			push(d)
		}
		if open[i].pos.Type == "PERP_FUNDING" && open[i].pos.OpenedAt != nil && e.cfg.PerpFundMinHold > 0 {
			push(open[i].pos.OpenedAt.Add(e.cfg.PerpFundMinHold))
		}
	}
	return earliest, found
}
