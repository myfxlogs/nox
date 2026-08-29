package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"
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

	qty, skip := computeBuyQty(opp, e.cfg.NotionalUSD, e.service.OppContractSize)
	if skip {
		// D-140 S2：CS-miss 按 (venue,canonical) 聚合计数
		e.recordCSMiss(opp.VenueBuy, opp.Canonical)
		return
	}
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

// isMTVenue 判断 venueKey 是否为 MT5/MT4 venue（D-135 sizing CS fail-closed 用）。
func isMTVenue(venue string) bool {
	return strings.HasSuffix(venue, ":mt5") || strings.HasSuffix(venue, ":mt4")
}

// computeBuyQty 计算买腿 qty（D-135：MT 腿乘 CS fail-closed，CEX 腿 CS=1 不变）。
// csResolver = e.service.OppContractSize（nil 接线时返回 ok=false → MT 腿 skip，CEX 腿正常）。
// 返回 skip=true 表示 fail-closed 跳过（MT 腿 CS 未知/未接线），调用方应 return。
func computeBuyQty(opp pgstore.Opportunity, notionalUSD float64, csResolver func(canonical, venueKey string) (float64, bool)) (qty float64, skip bool) {
	cs := 1.0
	if isMTVenue(opp.VenueBuy) { // MT 腿必须查到 CS（fail-closed，D-135）
		v, ok := csResolver(opp.Canonical, opp.VenueBuy)
		if !ok || v <= 0 {
			log.Printf("[autotrade] skip opp=%s: MT buy-leg CS unknown (fail-closed) canonical=%s venue=%s", opp.ID, opp.Canonical, opp.VenueBuy)
			return 0, true
		}
		cs = v
	}
	return notionalUSD / (opp.PriceBuy * cs), false
}

// === D-140 S2：CS-miss 聚合告警 ===

// csMissAlertInterval CS-miss 告警 flush 间隔（D-140 S2）。
const csMissAlertInterval = 10 * time.Minute

// recordCSMiss 记一次 CS-miss（D-140 S2）。按 (venue,canonical) 聚合，
// 定期 flushCSMissAlerts 发 info 告警（同 key 去重 ≥1h）。
func (e *Engine) recordCSMiss(venue, canonical string) {
	key := venue + "|" + canonical
	e.csMissMu.Lock()
	e.csMissCounts[key]++
	e.csMissMu.Unlock()
}

// flushCSMissAlerts 发出聚合 CS-miss 告警（D-140 S2）。
// 由 positionLoop 定期调用（csMissAlertInterval）；同 key 去重 ≥1h。
func (e *Engine) flushCSMissAlerts() {
	e.csMissMu.Lock()
	if len(e.csMissCounts) == 0 {
		e.csMissMu.Unlock()
		return
	}
	counts := e.csMissCounts
	e.csMissCounts = make(map[string]int)
	now := time.Now()
	// 去重：跳过 1h 内已发过的 key
	var lines []string
	for key, n := range counts {
		if last, ok := e.csMissSent[key]; ok && now.Sub(last) < time.Hour {
			continue
		}
		e.csMissSent[key] = now
		lines = append(lines, fmt.Sprintf("%s count=%d", key, n))
	}
	e.csMissMu.Unlock()
	if len(lines) == 0 {
		return
	}
	msg := "unmapped-or-no-CS: " + strings.Join(lines, "; ")
	log.Printf("[autotrade] %s", msg)
	if e.AlertSink != nil {
		e.AlertSink("info", msg)
	}
}
