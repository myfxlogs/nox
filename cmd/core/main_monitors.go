// cmd/core 监控器+dashboard+服务装配（从 main.go 拆出，保 main() ≤300 行）。
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"connectrpc.com/connect"

	"nox/internal/api/dashboard"
	"nox/internal/data/bus"
	"nox/internal/data/cexscan"
	"nox/internal/data/collect/exchange"
	"nox/internal/ledger/reconcile"
	"nox/internal/platform/safego"
	"nox/internal/portfolio/budget"
	"nox/internal/research/oppmatch"
	"nox/internal/store/pgstore"
	"nox/internal/trading/dciexec"

	dashv1 "nox/internal/gen/dashboardv1"
	dashv1connect "nox/internal/gen/dashboardv1/dashboardv1connect"
	"nox/internal/ledger/settleadapter"
)

// setupMonitorsAndServices 装配 6d~6g 监控器 + dashboard 服务 + auth + runServices。
// 返回 dashSvc（测试/其他 wiring 可能需要）。
func setupMonitorsAndServices(a monitorsSetupArgs) *dashboard.Service {
	ctx := a.ctx
	cfg := a.cfg
	st := a.st
	qbus := a.qbus
	brokerMgr := a.brokerMgr
	pipeline := a.pipeline
	recon := a.recon

	// 6d. Broker 实持仓 monitor（D-052 起；D-571 包A 事件化）：持仓视图单一真相源——
	// 事件驱动（MT5 帧 patch/CEX hook kick/订单 kick/qbus 报价联动/流状态 venue_online），
	// REST 仅权威点回读（boot/kick/未知票号/refresh RPC），逐仓 upsert|remove 广播 case 15 + 新订阅回放。
	bpTopic := bus.NewTopic[*dashv1.BrokerPositionEvent]() // D-571 包A：逐仓事件通道（case 15）
	// D-167 折分：bpMon 构造+setter 注入提取至 main_monitors_bpmon.go 保行数。
	bpMon, settlementCollector := setupBrokerPositionMonitor(a, bpTopic)

	// 6e. 账户资产 monitor（D-053/D-054）：账户视图单一真相源——CFD（brokerMgr AccountQueriers）+ CEX（DB exchange_configs 每轮重取）。
	// 复用 BrokerPosPollInterval 轮询节奏（不新增 knob）；变更广播 case 9。
	acctTopic := bus.NewTopic[*dashv1.AccountEvent]()            // D-571 包B：逐 venue 事件 case16
	acctMon := dashboard.NewAccountMonitor(brokerMgr, acctTopic) // D-571 包B：事件化（ticker 死净）
	acctMon.SetExchangeStore(st)                                 // D-054：CEX 交易所后台增删/改 key → 定向 kick/Refresh 生效，不重启
	// D-604 P2③：无流池消费点实拉钩——oppmatch 扫描拍/budget 日切拍内单次
	// Refresh→venue:type:ccy→equity 映射（拉败=池跳过+warn 留痕；禁 TTL 变相快照）。
	// 分币粒度修复：MULTI 户（earn 族）Equity=Σqty 指纹不入 map——分币键取
	// Holdings 原生量（live_equity_map.go）。
	liveMapFn := func(ctx context.Context) (map[string]float64, error) {
		return liveEquityMapFromAccounts(acctMon.Refresh(ctx)), nil
	}
	oppmatch.SetLiveEquityMapFn(liveMapFn)
	budget.SetLiveEquityMapFn(liveMapFn)
	dciexec.SetLiveEquityMapFn(liveMapFn) // 浓度透视分母实拉（earn.go liveConcentration）
	pgstore.SetLiveEquityMapFn(liveMapFn) // explorer_idle 「此刻权益」实拉（判定径清账）
	// D-571 包B：流状态事件双挂——bpMon（venue_online）+ acctMon（online 域分工不变）。
	brokerMgr.SetStreamStateSink(func(vk string, on bool) {
		bpMon.OnStreamState(vk, on)
		acctMon.OnStreamState(vk, on)
	})
	// D-107 P2 / D-071：margin 事件 → acctMon.Kick(venueKey) 定向无防抖直读
	// （margin 首采=空仓 MT5 账户唯一入态事件；全量防抖路径只留 Kick("")）。
	brokerMgr.SetOrderProfitKick(func(vk string) { acctMon.Kick(vk) })
	// D-445 S0：OnOrderUpdate 余额帧 → acctMon 流缓存（白名单卡资金列兜底+加温）。
	brokerMgr.SetAccountSink(accountSinkWithFreeMargin(ctx, st, acctMon.ObserveStreamAccount)) // D-006 S2：sink 挂 FreeMargin 双因子告警（判据在 freemargin_risk.go）
	brokerMgr.SetDealSink(mtDealIOSink(st, brokerMgr))                                         // D-607：OnOrderUpdate Deal 事件→fin_venue_io 主径
	safego.GoLoop("main:acctMon", func() { acctMon.Run(ctx) })
	// 3d-P1#1b：MT 余额帧事件 → fin_venue_account_snapshot 即写（src=mt_frame，1s/venue
	// 合流+同值不写）——cfd 键周期补写与 mt_event 双写退役；margin_guard 经 snapHook 随帧评估。
	safego.GoLoop("main:acctFrameSnapshots", func() { cexscan.StartAccountEventSnapshots(ctx, acctTopic, st, acctMon) })

	// D-271 S5：CEX 只读扫描器（只读 API key，不下单不触 D-158）。
	cexScanner, err := buildCexScanner(cfg, st, acctMon, a.refreshTopic)
	if err != nil {
		log.Printf("[wiring] cexscan build failed: %v (skipped)", err)
	} else {
		startCexScanner(cexScanner, ctx, cfg)
	}
	// D-450：订单/持仓事件缓存（WS 加速层，REST 兜底）——exec/pos 两管理器+OKX 私有流共享同一实例（D-469a）。
	cexOrderCache := exchange.NewOrderEventCache()
	cexPosCache := exchange.NewPositionEventCache()
	// D-469b S1：OKX 私有流生命周期归 CexStreamManager（boot 一次性装配已移除——
	// config 驱动重连；hooks/缓存共享经 cexStreamMgr.OKXHooks+oc/pc 同实例注入）。

	// D-107 P2：orderTopic 事件 → bpMon.Kick（OnOrderUpdate 持仓变更 → 触发回读，pollTicker 兜底不变）。
	// 3d-P1#1b：订单事件快照双写（mt_event）已退役——余额帧事件写口（acctFrameSnapshots）覆盖。
	safego.GoLoop("main:bpMon.orderKick", func() {
		evCh, cancel := a.orderTopic.Subscribe()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-evCh:
				if !ok {
					return
				}
				bpMon.Kick(ev.Venue) // D-571 包A：venue 定向回读（裸名→全部类型键）
			}
		}
	})

	// 6f. CEX 下单执行器（D-059）：exchange_configs 每轮重取 → 注册/注销进 execReg。
	// 镜像 AccountMonitor 语义——后台增删/改 key → 下轮 tick 生效，不重启。
	// D-108 P2 T1：CEX 平仓被拒 critical 告警（AlertFn 适配 store.InsertAlert 签名差 ctx）。
	// D-301c S2：同指纹冷却包装——posID 不入指纹（实例明细非类别，D-301c §2）。
	cexAlertFn := a.alertCooler.Wrap3(func(posID *int64, level, msg string) {
		// D-411：posID 实参走持仓级告警列（rule_id FK→rules，塞 posID 曾静默丢告警）。
		if posID != nil {
			if err := st.InsertPositionAlert(context.Background(), *posID, level, msg); err != nil {
				log.Printf("[cex-alert] insert position alert: %v", err)
			}
			return
		}
		if err := st.InsertAlert(context.Background(), nil, level, msg); err != nil {
			log.Printf("[cex-alert] insert alert: %v", err)
		}
	})
	cexExecMgr := dashboard.NewCexExecutorManager(st, a.execReg, 60*time.Second, cexAlertFn, cfg.MarginReservePct) // D-469b S2：kick 主+60s 兜底
	wireDephealth(st, a.alertCooler)                                                                               // D-579 S6：dephealth 告警道装配（wire_dephealth.go）
	cexExecMgr.SetLiqGuards(cfg.MinLiqDistancePct, cfg.CrossNotionalEquityCap)                                     // D-472 S4
	cexExecMgr.SetSpotTakerBps(cfg.CommissionTaker, cfg.CommissionCEXTakerBps)                                     // D-473 S2
	// D-421 §1.5：exchange_configs 刷新镜像订单级查单器 → recon 消解器/收编守卫。
	cexExecMgr.OrderCache = cexOrderCache
	cexExecMgr.OrderStatusHook = func(qs map[string]reconcile.OrderStatusQuerier) { recon.SetOrderStatusQueriers(qs) }
	safego.GoLoop("main:cexExecMgr", func() { cexExecMgr.Run(ctx) })

	// 6f2. CEX perp 持仓查询器（D-121）：exchange_configs 每轮重取 → CEXPositionQuerier → BrokerManager.SetCEXQueriers。
	// BrokerPositionMonitor 轮询这些 querier → CEX perp 持仓出现在前端 Broker 实持仓 + 持仓表浮盈/funding/净值。
	cexPosMgr := dashboard.NewCexPositionManager(brokerMgr, st, 60*time.Second) // D-469b S2：kick 主+60s 兜底
	cexPosMgr.PositionCache = cexPosCache
	safego.GoLoop("main:cexPosMgr", func() { cexPosMgr.Run(ctx) })

	// 6f3. CEX 私有 WS 流（D-450）：gate/binance 订单+持仓+余额事件驱动；REST 轮询双轨保留。
	// D-469a S2：事件缓存写面 kick 闭环——订单事件同时叫醒 engine（orderTopic 已订阅
	// →atEngine.Kick）与 bpMon；持仓事件 kick bpMon。Ticket=0=通用通知语义（全量回读）。
	cexOrderCache.SetOnPut(func(vk string) {
		a.orderTopic.Publish(bus.OrderEvent{Venue: vk, Ticket: 0})
		bpMon.Kick(vk)
	})
	cexPosCache.SetOnPut(func(vk string) { bpMon.Kick(vk) })                                          // D-571 包A：CEX 持仓事件→venue 定向回读（CachedVenueQuerier 新鲜窗直返=WS 数据落视图）
	cexExecMgr.KickTopic = a.configKick                                                               // D-469b S2
	cexPosMgr.KickTopic = a.configKick                                                                // D-469b S2
	cexStreamMgr := dashboard.NewCexStreamManager(st, st, 60*time.Second, cexOrderCache, cexPosCache) // D-469b S2：kick 主+60s 兜底
	// D-542：CEX 账户可用资金活账本——私有流喂入（事件主源）与 dashboard 弹窗实拉校准共享同实例。
	balLedger := bus.NewBalanceLedger()
	cexStreamMgr.BalLedger = balLedger
	cexStreamMgr.KickTopic = a.configKick             // D-469b S2
	cexStreamMgr.OKXHooks = exchange.OKXPrivateHooks{ // D-469b S1：OKX 流纳管后 hooks 挂 manager
		OnFundingFee:  func() { kickChanSend(a.okxIncomeKickCh) },
		OnAccountPush: func() { acctMon.Kick("okx") }, // D-571 包B：venue 定向（hooks 内隐含 okx）
		OnOrderPush:   func() { bpMon.Kick("okx") },   // D-571 包A：venue 定向
		// D-566 包B S7：爆仓预警 critical（binance MARGIN_CALL 同通道先例）。
		OnLiqWarning: func(instID, posSide, mgnRatio string) {
			cexAlertFn(nil, "critical", fmt.Sprintf("okx 爆仓预警 inst=%s posSide=%s mgnRatio=%s", instID, posSide, mgnRatio))
		},
	}
	// D-469a S3/S4：余额快照 kick acctMon；binance a.m 事件原因 kick income/io 通道。
	cexStreamMgr.AcctKick = func(vk string) { acctMon.Kick(vk) }         // D-571 包B：venue 定向
	cexStreamMgr.AcctResync = func(vk string) { acctMon.KickResync(vk) } // D-523②：重连回环→快照 resync
	brokerMgr.SetAcctResync(func(vk string) { acctMon.KickResync(vk) })  // D-523②：mt5 会话重连同径
	cexStreamMgr.BinanceHooks = exchange.BinanceUserHooks{
		OnFundingFee: func() { kickChanSend(a.incomeKickCh) },
		OnIO:         func() { kickChanSend(a.ioKickCh) },
		// D-569 包B S7：MARGIN_CALL 整帧 critical（截断 500 防巨帧）。
		OnMarginCall: func(payload []byte) {
			msg := string(payload)
			if len(msg) > 500 {
				msg = msg[:500]
			}
			cexAlertFn(nil, "critical", "binance 保证金追缴（margin call）\n原始明细："+msg)
		},
	}
	wireVenueCloseHooks(cexStreamMgr, a.recon)                       // D-015 ②（R1）：okx/binance 平仓事件 → recon.Kick（不直写腿）
	wireGateStreamHooks(a.gateWS, cexStreamMgr, cexAlertFn, a.recon) // D-570 包C S6⑤/S7+D-015 ②：gate 失效闭包+风险频道 hooks（拆 wire_gate.go 保行数；平仓事件入对账）
	safego.Go("main:cexStreamMgr", func() { cexStreamMgr.Run(ctx) })

	// 6g. MT 数据采集健康监控（D-104 P0）：构造提取至 setupMTHealth（D-112 S5.2）。
	mtHealth := setupMTHealth(brokerMgr, qbus, st, cfg)
	safego.GoLoop("main:mtHealth", func() { mtHealth.Run(ctx) })
	// 6g2. 表级新鲜度监控（D-301a S1）：8 表白名单 + 三态转换式告警，tick=60s。
	// 兜低频入库表暗死（fin_cmc_* 断流/funding_history okx 停滞 45.8d 实证）。
	tableHealth := setupTableHealth(st)
	safego.GoLoop("main:tableHealth", func() { tableHealth.Run(ctx) })
	// 6g3. CEX 静态代理健康钉（D-301b S2）：透传 ping 探测单点代理，3 连败 crit+恢复 info；
	// 兜「代理 VPS 挂=CEX 全盲」；BINANCE_PROXY_URL 空 → 不启动 fail-open（wire_health.go）。
	startCexProxyHealth(ctx, st, cfg)
	startEgressProbe(ctx, st, cfg) // 6g4 D-011 S1：出口直连链路健康探针（三域探活+聚合根因告警，wire_health.go）
	dashSvc := dashboard.New(st, qbus, pipeline, brokerMgr, a.reviewRun, cfg.DetectorFreshnessTTL, cfg.LiqMMR, cfg.FundingDropRatio, cfg.SwapArbEquityUseRatio, cfg.SwapArbBareCap, cfg.SwapArbVenueExposureCap, a.oppTopic, a.alertTopic, bpMon, cfg.OpportunityNotionalUSD, acctMon, a.commResolver, cfg.SlippageBps, cfg.XAssetCexFeeBps, a.kickFn, cfg.CapitalBaseUSD)
	// D-571 包A 事件载荷标注（D-009 S3 净估需 svc 的 cs/fx/费率链，故 svc 建成后接线）。
	bpMon.SetAnnotator(func(ctx context.Context, bps []*dashv1.BrokerPosition) {
		dashboard.AnnotateBrokerPositions(ctx, dashSvc, bps)
	})
	if pipeline != nil { // D-010：venue 保证金实占闸+腿保证金率缝接执行（守卫锁内复查=展示闸6 同函数）
		pipeline.SetMarginInputs(dashSvc.VenueSystemMarginUSD, dashSvc.LegMarginFrac)
	}
	dashSvc.SetPerpLeverageTarget(cfg.PerpLeverageTarget)                                // D-079：perp 腿保证金口径按策略杠杆（引擎开仓前置 ensure 的真实状态）
	dashSvc.SetAppConfigView(cfg, a.acDefaults, a.acEnvSnap)                             // D-523：app_config 键视图三快照
	dashSvc.SetBalanceLedger(balLedger)                                                  // D-542：弹窗腿池实拉（私有流主源+REST 校准）
	wirePositionMonitor(ctx, dashSvc, a, bpTopic, bpMon, brokerMgr, settlementCollector) // D-571 包C/R2：posMon 四域发射器（拆 wire_posmon.go 保行数）
	balLedger.SetOnPut(func(vk string) {                                                 // D-568 funds 通知 + D-571 包B 账本命中 patch/kick 兜底
		dashSvc.NotifyFunds()
		acctMon.OnLedgerAcct(vk)
	})
	acctMon.SetBalanceLedger(balLedger)                                                 // D-571 包B：CEX 余额活账本注入
	acctMon.SetFundsHook(dashSvc.NotifyFunds)                                           // D-568：MT5 流帧+轮询变更→funds 通知
	acctMon.SetSnapshotWriter(st.InsertVenueAccountSnapshots)                           // D-132：REST-only CEX 面值变→审计留痕搭车写口
	acctMon.SetEarnYieldStore(st)                                                       // D-524：earn 池收益增量归属入账（快照写径同拍）
	dashSvc.SetSecretResolver(a.secRes)                                                 // D-545：密钥管理面+热读轨注入
	dashSvc.SetCrossCap(cfg.CrossNotionalEquityCap)                                     // D-478 S4
	cexStreamMgr.TierKickFn = func() { dashSvc.TierKickAll(ctx) }                       // D-478 S4：余额快照→档位重算（恶化转移告警）装配（*pgstore.Store 满足 alertInserter）
	safego.GoLoop("main:pendingGateWatch", func() { dashSvc.RunPendingGateWatch(ctx) }) // D-007 S1：闸态 diff→pending_entries
	safego.GoLoop("main:autoMapSweep", func() { dashSvc.RunAutoMapSweep(ctx) })         // D-148：broker 目录周期重识别（4h）
	// D-405 S3：保证金三档评估器——snapshot 写口钩子（事件驱动，三写口收敛单钩）。
	dashSvc.InitMarginGuard(cfg.MarginUrgentCooldown, cfg.MarginWarnCooldown)
	startTrxDailyRollup(ctx, dashSvc)                                                                               // D-211 §5：TRX 日终 rollup ticker + boot 补跑昨日
	safego.GoLoop("main:idleCapital", func() { dashboard.NewIdleCapitalMonitor(dashSvc, 30*time.Minute).Run(ctx) }) // D-554
	// D-087 §3.2：settlement.Settle 统一结算入口——构造 storeDeps（实现 settlement.Deps）注入三调用点
	settleDeps := settleadapter.New(st)
	// D-249①：淘汰多空听证钩（裁定击杀前对辩落 fin_strategy_hearing；观察面 fail-open）。
	settleDeps.HearElim = newElimHearing(st, cfg)
	recon.SetCloser(dashSvc)
	recon.SetSettler(settleDeps) // D-087：替换 SetStatUpdater + SetVerificationUpdater
	dashSvc.SetSettler(settleDeps)
	pipeline.SetSettler(settleDeps)                              // D-193-A 扩包 S5：手动 ClosePosition/ResumeClose 路径也触发结算（修复 479 缺 settlement 行）
	wireExecInjections(pipeline, st, recon, cfg, a.commResolver) // 修复⑫：exec 注入（PnL 校准+CS 复用+fx 换算+D-159 组合门禁+D-599 费率链）
	wireReconKicks(ctx, a, recon, cexStreamMgr)                  // D-004 P1#6：对账事件化（kick 订阅+重连钩+事件循环）
	wireReconFastRetry(ctx, recon, cfg)                          // D-407：残腿密集重试装配（拆 wire_posmon.go 保行数）
	// dashSvc setter 注入（leg 查询/CS/frontConfig/analyst）提取至 wire_dashboard.go 保 main() ≤300 行。
	wireDashboardSetters(ctx, dashSvc, cfg, st, brokerMgr, a.factCache, a.feishuClient)
	if a.cmcWs != nil { // D-212：cmc ws LiveState/健康注入（nil=REST 兜底——freshLive 分域 DB 兜底径）
		dashSvc.SetCmcLiveProvider(a.cmcWs.LiveState)
		dashSvc.SetCmcWsHealth(a.cmcWs.IsHealthy)
	}
	wirePosreview(ctx, st, cfg, a) // D-266 M4.5：复盘员（posChange 订阅+存量补扫）
	// D-163：探针机审——kick 驱动 + boot 扫（事件唯一正道，无定时轮询）。
	safego.GoLoop("main:probeAdjudicator", func() {
		sub, cancel := a.probeTopic.Subscribe()
		defer cancel()
		dashSvc.RunProbeAdjudicator(ctx, sub)
	})
	authIntercept := wireAuthIntercept(a.secRes, a.cfg) // D-095：auth 热读装配（双空 fatal fail-closed）
	path, handler := dashv1connect.NewDashboardServiceHandler(dashSvc,
		connect.WithInterceptors(authIntercept),
	)
	// D-620⑦：真金单闸注入（lg/cp 由 main() 早建单实例经 args 下穿——oppmatch 先于此处启动）。
	dashSvc.SetGateChecker(a.cp)
	dashSvc.SetConfigKick(func() { a.configKick.Publish(struct{}{}) }) // D-469b S2/S5
	// D-033：系统自盯汇报面（日报 08:00BJ / 周一 08:00BJ 周报=D-032 ④ 移入 / 小时级数据面告警）。
	startSelfwatch(ctx, st, a.feishuClient, cfg.CapitalBaseUSD, a.cp)
	// D-017 R1：xassetAllowBind=atEngine 装配后接通 detector 白名单前置滤（延迟绑定收口）。
	runServices(ctx, a.cancel, cfg, st, dashSvc, a.oppTopic, a.orderTopic, qbus, a.factsDone, a.dsn, path, handler, settleDeps, settlementCollector, a.execReg, a.liveGate, a.cp, a.feishuClient, a.secRes, a.configKick, a.refreshTopic, a.pipeline, a.xassetAllow.Bind)
	return dashSvc
}
