// cmd/core Broker 实持仓 monitor 装配（D-167 折分：自 main_monitors.go 提取保行数，零行为变更）。
package main

import (
	"context"
	"log"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/bus"
	"nox/internal/data/collect/exchange"
	"nox/internal/data/fact"
	"nox/internal/data/keydomain"
	"nox/internal/platform/safego"

	dashv1 "nox/internal/gen/dashboardv1"
)

// setupBrokerPositionMonitor 6d 段：bpMon 构造+全部 setter 注入+Run 起 goroutine（提取自
// setupMonitorsAndServices）。返回 bpMon 与 settlementCollector（后续装配点消费）。
func setupBrokerPositionMonitor(a monitorsSetupArgs, bpTopic *bus.Topic[*dashv1.BrokerPositionEvent]) (*dashboard.BrokerPositionMonitor, *dashboard.SettlementProfileCollector) {
	ctx := a.ctx
	cfg := a.cfg
	st := a.st
	qbus := a.qbus
	brokerMgr := a.brokerMgr

	// D-337R3 S1：cexIntervals boot Populate（best-effort，失败回退 warn=D-079 禁静默；
	// 与 wire_income 各自持有=最简，同一只读端点多拉一次成本可忽略）。
	cexIntervals := exchange.NewFundingIntervalCache()
	if err := cexIntervals.Populate(ctx, a.cexCfg); err != nil {
		log.Printf("[wiring] monitor cexIntervals populate (best-effort, CEX 腿倒计时 8h 回退): %v", err)
	}
	// D-466 S3：腿级 venue 原生结算时刻/周期 facts 读面（funding_next_settle+funding_interval，
	// 三 CEX 采集器同帧发出）——返回零值=诚实缺，build 侧三级回退。
	cexNextSettle := func(ctx context.Context, venue, symbol string) (time.Time, time.Time, time.Duration) {
		var next, ts time.Time
		var ivl time.Duration
		if fs, err := st.LatestFacts(ctx, fact.KindFundingNextSettle, venue, symbol); err == nil && len(fs) > 0 && fs[0].Value > 0 {
			next = time.Unix(int64(fs[0].Value), 0).UTC()
			ts = fs[0].Ts
		}
		if fs, err := st.LatestFacts(ctx, fact.KindFundingInterval, venue, symbol); err == nil && len(fs) > 0 && fs[0].Value > 0 {
			ivl = time.Duration(fs[0].Value * float64(time.Second))
		}
		return next, ts, ivl
	}
	// D-469a S5：事件路径已覆盖（orderTopic+posCache kick），poll 降 30s 兜底（cfg 本体不动，三件套 469b 统一降）。
	// D-571 包A：poll/quote 定时器移除——事件驱动（帧 patch/kick/报价联动/流状态）。
	bpMon := dashboard.NewBrokerPositionMonitor(brokerMgr, qbus, cfg.DetectorFreshnessTTL, bpTopic,
		dashboard.NewServerTZCache(24*time.Hour, serverTZFetcherFor(brokerMgr)), cexIntervals, cexNextSettle) // D-337R2/R3 S1+D-466：构造必选注入（编译期装配守卫）
	// D-571 包A：MT5 OnOrderProfit 帧逐仓 patch + 流状态透出（venue_online 流承载）；
	// 事件载荷标注（position_id/order_id/alias/CEX swap）与 case 8 同源三件套闭包。
	brokerMgr.SetPositionsSink(bpMon.OnMT5Frame)
	// D-337R4 S1：CEX 腿周期费率源接线（零调用方第 5 现修复）——facts 最新年化值，÷(365×periods) 换算在 build 侧。
	bpMon.SetCEXFundingRate(func(ctx context.Context, venue, symbol string) (float64, bool) {
		fs, err := st.LatestFacts(ctx, "funding", venue, symbol)
		if err != nil || len(fs) == 0 {
			return 0, false
		}
		return fs[0].Value, true
	})
	bpMon.SetSwapSnapshotStore(st) // D-110 P2：swap 快照采集注入
	// D-162 c：MT5 休市/重启后展示末价兜底——ticks 表持久化末帧（venueType=键型，
	// 限展示径消费不喂判定；decisions.md D-162 留裁）。
	bpMon.SetStaleTickFallback(func(ctx context.Context, venueKey, symbol string) (bus.Quote, bool) {
		kd := keydomain.Parse(venueKey)
		t, ok, err := st.LatestTick(ctx, kd.Venue, kd.Type, symbol)
		if err != nil || !ok {
			return bus.Quote{}, false
		}
		return bus.Quote{Symbol: symbol, Venue: kd.Venue, VenueType: kd.Type, Bid: t.Bid, Ask: t.Ask, Time: t.Ts}, true
	})
	// D-036：MT5 仓位级保证金=规格估算（D-031 ② RequiredMargin RPC 径已拆——生产网关
	// Unimplemented）。率径输入=账户级 init_margin_rate fact（裸 venue 键）。
	bpMon.SetInitMarginRateFor(func(ctx context.Context, venue string) (float64, bool) {
		fs, err := st.LatestFacts(ctx, fact.KindInitMarginRate, venue, "")
		if err != nil || len(fs) == 0 || fs[0].Value <= 0 {
			return 0, false
		}
		return fs[0].Value, true
	})
	brokerMgr.SetBPM(bpMon) // D-121：CEX 平仓需查 bpm 快照获取 side/volume
	// D-180 S1：结算画像采集器注入 + boot 存量回溯（扫全历史跳变回填 swap_settled facts）。
	settlementCollector := dashboard.NewSettlementProfileCollector(st)
	bpMon.SetSettlementProfileCollector(settlementCollector)
	safego.Go("main:settlementBackfill", func() {
		if err := settlementCollector.BackfillFromHistory(ctx); err != nil {
			log.Printf("[settlement-profile] backfill: %v", err)
		}
	})
	safego.GoLoop("main:bpMon.Run", func() { bpMon.Run(ctx) })
	return bpMon, settlementCollector
}
