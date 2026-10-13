// wire_posmon.go —— D-571 包C：posMon 行级事件发射器装配（自 main_monitors.go 拆出保行数）。
// positions(case4)/sourceHealth(case5)——hedge/personal 域随族件剔除拆除。
// kick 源+流状态扇出，全事件驱动零定时器。
package main

import (
	"context"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/bus"
	"nox/internal/data/collect/exchange"
	dashv1 "nox/internal/gen/dashboardv1"
	"nox/internal/infra/dephealth"
	"nox/internal/ledger/reconcile"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
)

// wirePositionMonitor 创建 posMon 四域发射器+挂接 dashSvc 回放/订阅口+三 kick 订阅+流状态第三挂点。
func wirePositionMonitor(ctx context.Context, dashSvc *dashboard.Service, a monitorsSetupArgs, bpTopic *bus.Topic[*dashv1.BrokerPositionEvent], bpMon *dashboard.BrokerPositionMonitor, brokerMgr *dashboard.BrokerManager, settlementCollector *dashboard.SettlementProfileCollector) {
	pm := dashboard.NewPositionMonitor(dashSvc,
		bus.NewTopic[*dashv1.Position](),
		bus.NewTopic[*dashv1.SourceHealth]())
	dashSvc.SetPositionMonitor(pm)
	// kick ① positions/personal 表写点（posChange 总线——pgstore.OnPositionChange 发布）。
	safego.GoLoopPlain("main:posMonC.posChange", func() {
		ch, cancel := a.posChange.Subscribe()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				pm.Kick()
			}
		}
	})
	// kick ② bpMon 逐仓事件（派生列 floating/net 依赖 broker 匹配联动重算——行本体未必变，diff 兜底）。
	safego.GoLoopPlain("main:posMonC.bp", func() {
		ch, cancel := bpTopic.Subscribe()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				pm.KickPositions()
			}
		}
	})
	// kick ④ 流状态第三挂点（sourceHealth 域——venue_online 翻转+WS 重连计量
	// 变化时重组装）。D-571C-R2 F-1：reconnectMeter.record 尾部扇出+在线态读源
	// 注入 assembleSourceHealth（否则踢了也无 diff=死踢）。
	prevStateSink := brokerMgr.StreamStateSinkFn()
	brokerMgr.SetStreamStateSink(func(vk string, on bool) {
		if prevStateSink != nil {
			prevStateSink(vk, on)
		}
		pm.OnStreamState(vk, on)
	})
	exchange.OnReconnectEvent = pm.OnHealthDirty
	// D-579-R3 F-3a：dephealth 状态变迁扇出 → sourceHealth 重组装（变迁才踢天然去重）。
	dephealth.OnChange = pm.OnHealthDirty
	dashSvc.SetStreamOnlineSource(bpMon.StreamOnlineSnapshot)
	settlementCollector.SetRefreshTopic(a.refreshTopic) // swap_facts 实写→case14
}

// wireReconKicks D-004 P1#6：对账事件源装配——订单事件（orderTopic）/ positions 写点
// （posChange）订阅 → recon.Kick；MT5 会话重连 OnReconnect 经 brokerMgr dispatch 晚绑定；
// CEX 私有流重连就绪经 CexStreamManager.ReconKick 透传。5min 定期对账 ticker 已删；
// 断流期漏事件由 venue 流重连 kick 兜（无看门狗裁定，见 internal/reconcile/reconcile_loop.go）。
func wireReconKicks(ctx context.Context, a monitorsSetupArgs, recon *reconcile.Reconciler, cexMgr *dashboard.CexStreamManager) {
	reconKickSubscribe(ctx, a.orderTopic, recon.Kick)
	reconKickSubscribe(ctx, a.posChange, recon.Kick)
	a.brokerMgr.SetReconKick(recon.Kick)
	cexMgr.ReconKick = recon.Kick // 须在 mgr.Run goroutine 启动前（调用点序满足）
	safego.GoLoop("main:recon", func() { recon.StartEventLoop(ctx) })
}

// wireReconFastRetry D-407：残腿密集重试装配（自 main_monitors.go 拆出保行数）——
// 30s tick×10min 窗；UNCERTAIN 不重试，窗外静默交还 30min 看护。
func wireReconFastRetry(ctx context.Context, recon *reconcile.Reconciler, cfg *config.Config) {
	retryWindow := 10 * time.Minute
	if cfg.ResidualRetryWindow > 0 {
		retryWindow = cfg.ResidualRetryWindow
	}
	retryConsec := 3
	if cfg.ResidualRetryConsec > 0 {
		retryConsec = cfg.ResidualRetryConsec
	}
	recon.SetFastRetryParams(retryWindow, retryConsec)
	safego.GoLoopPlain("main:reconFastRetry", func() { recon.StartFastRetryLoop(ctx) })
}

// reconKickSubscribe topic 订阅扇出到 kick（事件载荷不消费——只作叫醒信号）。
func reconKickSubscribe[T any](ctx context.Context, topic *bus.Topic[T], kick func()) {
	safego.GoLoopPlain("main:recon.kick", func() {
		ch, cancel := topic.Subscribe()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				kick()
			}
		}
	})
}
