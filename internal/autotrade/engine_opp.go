package autotrade

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/store/pgstore"
)

// oppLoop 订阅机会事件 → 过滤 → 产 intent 走 gate（D-072：风控由 gate 统一）。
// D-080 R7：每 loop 一个长生命周期 accumulator——当日累计、轮末 flush 覆盖写；
// 日切由 accumulator 内部 pending 队列处理（零定时器，无共享竞态）。
func (e *Engine) oppLoop(ctx context.Context) {
	ch, unsub := e.oppTopic.Subscribe()
	defer unsub()
	acc := newComboAccumulator()
	for {
		select {
		case <-ctx.Done():
			return
		case opp, ok := <-ch:
			if !ok {
				return
			}
			log.Printf("[autotrade] recv opp=%s type=%s netBps=%.2f buy=%s sell=%s", opp.ID, opp.Type, opp.NetBps, opp.VenueBuy, opp.VenueSell)
			e.handleOpportunity(ctx, opp, acc)
			if err := acc.Flush(ctx, e.store); err != nil {
				log.Printf("[autotrade] opp rollup flush: %v", err)
			}
		}
	}
}

// handleOpportunity 过滤机会 → 产 intent 走 gate（D-072：风控由 gate 统一）。
// D-080 R7：acc 由调用方传入（每 loop 一个长生命周期实例，处理完 flush 覆盖写）。
// D-120（2026-08-27）：解除 D-070「放弃 CEX」限制——CFD 机会枯竭（SWAPARB 0 条新），
// PERP_FUNDING 通道已修复（gate testnet 域名迁移+key 验证通过），恢复 CEX 机会执行。
func (e *Engine) handleOpportunity(ctx context.Context, opp pgstore.Opportunity, acc *comboAccumulator) {
	// D-080 S4：全组合喂入 rollup 累积器（含低于门槛——覆盖度完整，write-behind）
	acc.Record(opp.ID, opp.Type, opp.NetBps, time.Now())

	// D-072 §3.4：排除品种从 env 读取
	if e.cfg.ExcludeCanonicals[opp.Canonical] {
		return
	}
	// D-072 §3.4：venue 白名单从 env 读取
	if !e.cfg.AllowVenues[opp.VenueBuy] || !e.cfg.AllowVenues[opp.VenueSell] {
		return
	}
	// D-072 §3.4：symbol 白名单从 env 读取
	if len(e.cfg.AllowCanonicals) > 0 && !e.cfg.AllowCanonicals[opp.Canonical] {
		return
	}
	// 门槛：净收益
	if opp.NetBps < e.cfg.MinNetBps {
		return
	}
	log.Printf("[autotrade] pass filter opp=%s type=%s netBps=%.2f", opp.ID, opp.Type, opp.NetBps)

	qty := e.cfg.NotionalUSD / opp.PriceBuy
	if qty <= 0 {
		return
	}
	// H4.2：qty 低于买腿最小步 → debug 跳过（品种适配事实，不产 intent 不落 REJECTED）
	// venueSymbol 从 qbus 取（handleOpportunity 无直接 quote，用 opp.VenueBuy 查）
	if qbuy, ok := e.quotes.Latest(opp.Canonical, opp.VenueBuy); ok {
		if e.service.QtyBelowMinStep(opp.VenueBuy, qbuy.VenueSymbol, qty) {
			log.Printf("[autotrade] skip opp=%s: qty %.6f below min step (notional $%.0f too small for %s)",
				opp.ID, qty, e.cfg.NotionalUSD, qbuy.VenueSymbol)
			return
		}
	}
	intent := TradeIntent{
		Strategy:    opp.Type,
		Canonical:   opp.Canonical,
		VenueBuy:    opp.VenueBuy,
		VenueSell:   opp.VenueSell,
		Qty:         qty,
		NotionalUSD: e.cfg.NotionalUSD,
		DedupKey:    opp.ID,
		NetBps:      opp.NetBps,
		Execute: func(ctx context.Context) (string, string, int64) {
			reqID := fmt.Sprintf("autotrade-%d-%s", time.Now().UnixMilli(), opp.ID)
			return e.service.ExecuteOpportunityInternal(ctx, opp.ID, reqID, qty)
		},
	}
	e.gate.Submit(ctx, intent)
}
