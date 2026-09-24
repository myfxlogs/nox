package autotrade

import (
	"context"
	"fmt"
	"log"
	"sort"
	"time"

	"arb/internal/combo"
)

// meanRevertLoop D-072 均值回归扫描：venue 对与阈值从 env 读取（§3.4 外部化）。
func (e *Engine) meanRevertLoop(ctx context.Context) {
	if e.quotes == nil {
		return
	}
	if len(e.cfg.MeanRevertThresh) == 0 {
		log.Println("[autotrade] meanRevertLoop: no thresholds configured (ARB_MEANREVERT_THRESHOLDS empty)")
		return
	}
	// venue 对从 AllowVenues 提取（前两个；D-443 热读 holder，sort 确定性语义不变）
	allow := e.allowVenues()
	venues := make([]string, 0, len(allow))
	for v := range allow {
		venues = append(venues, v)
	}
	sort.Strings(venues)
	if len(venues) < 2 {
		log.Println("[autotrade] meanRevertLoop: need >=2 allow venues (ARB_AUTOTRADE_ALLOW_VENUES)")
		return
	}
	venueA, venueB := venues[0], venues[1]
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	// D-080 R7：每 loop 一个长生命周期 accumulator——当日累计、轮末 flush 覆盖写；
	// 日切由 accumulator 内部 pending 队列处理（零定时器，无共享竞态）。
	acc := newComboAccumulator()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// D-080 S4 N9 修订：判死策略停扫改为停提交——扫描+评估常开喂 rollup
			e.scanMeanRevert(ctx, e.cfg.MeanRevertThresh, venueA, venueB, acc)
			if err := acc.Flush(ctx, e.store); err != nil {
				log.Printf("[autotrade] meanRevert rollup flush: %v", err)
			}
		}
	}
}

// scanMeanRevert 扫描价差，超阈值则产 intent 走 gate（D-072：风控由 gate 统一）。
// H4.1：满仓短路——CountOpenPositions >= MaxPositions 整轮跳过（省逐组合产 intent 撞 gate 的日志噪音）。
// D-080 R7：acc 由调用方传入（每 loop 一个长生命周期实例，轮末 flush 覆盖写）。
func (e *Engine) scanMeanRevert(ctx context.Context, symbols map[string]float64, venueA, venueB string, acc *comboAccumulator) {
	if e.maxPositionsReached(ctx) {
		return
	}
	for canonical, threshold := range symbols {
		qA, okA := e.quotes.Latest(canonical, venueA)
		qB, okB := e.quotes.Latest(canonical, venueB)
		if !okA || !okB {
			continue
		}
		now := time.Now()
		// D-077 修复⑪B N6：陈旧度复用 DetectorFreshnessTTL（env 化）
		if e.cfg.FreshnessTTL > 0 {
			if now.Sub(qA.Time) > e.cfg.FreshnessTTL || now.Sub(qB.Time) > e.cfg.FreshnessTTL {
				continue
			}
		}
		spreadAB := qB.Bid - qA.Ask
		spreadBA := qA.Bid - qB.Ask
		if spreadAB > threshold {
			bps := spreadAB / qA.Ask * 10000
			// D-080 S4：全组合喂入 rollup 累积器（含低于门槛——覆盖度完整）
			acc.Record(combo.Key("MEANREVERT", canonical, venueA, venueB), "MEANREVERT", bps, now)
			e.executeMeanRevert(ctx, canonical, venueA, venueB, bps)
		} else if spreadBA > threshold {
			bps := spreadBA / qB.Ask * 10000
			acc.Record(combo.Key("MEANREVERT", canonical, venueB, venueA), "MEANREVERT", bps, now)
			e.executeMeanRevert(ctx, canonical, venueB, venueA, bps)
		}
	}
}

// executeMeanRevert 产 TradeIntent 走 gate（D-072：不直连执行）。
func (e *Engine) executeMeanRevert(ctx context.Context, canonical, venueBuy, venueSell string, openBps float64) {
	// D-080 S4 N9 修订：判死策略停提交（扫描+评估常开喂 rollup，禁用只拦提交无 REJECTED 行）
	// D-190：err → log + return（fail-closed，与 gate.go:75 一致）
	disabled, err := e.store.IsStrategyDisabled(ctx, "MEANREVERT")
	if err != nil {
		log.Printf("[autotrade] IsStrategyDisabled error (fail-closed skip): %v", err)
		return
	}
	if disabled {
		return
	}
	qbuy, ok := e.quotes.Latest(canonical, venueBuy)
	if !ok {
		return
	}
	qty := e.cfg.NotionalUSD / qbuy.Ask
	// H4.2：qty 低于买腿最小步 → debug 跳过（品种适配事实，不产 intent 不落 REJECTED）
	if e.service.QtyBelowMinStep(venueBuy, qbuy.VenueSymbol, qty) {
		log.Printf("[autotrade] MEANREVERT skip %s: qty %.6f below min step (notional $%.0f too small for %s)",
			canonical, qty, e.cfg.NotionalUSD, qbuy.VenueSymbol)
		return
	}
	dedupKey := combo.Key("MEANREVERT", canonical, venueBuy, venueSell)
	intent := TradeIntent{
		Strategy:    "MEANREVERT",
		Canonical:   canonical,
		VenueBuy:    venueBuy,
		VenueSell:   venueSell,
		Qty:         qty,
		NotionalUSD: e.cfg.NotionalUSD,
		DedupKey:    dedupKey,
		NetBps:      openBps,
		Execute: func(ctx context.Context) (string, string, int64) {
			reqID := fmt.Sprintf("mr-%d", time.Now().UnixNano())
			return e.service.ExecuteMeanRevert(ctx, canonical, venueBuy, venueSell, reqID, qty, openBps)
		},
	}
	e.gate.Submit(ctx, &intent)
}
