package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/bus"
	"nox/internal/data/collect"
	"nox/internal/data/fact"
	"nox/internal/data/keydomain"
	"nox/internal/data/normalize"
	"nox/internal/data/venue"
	"nox/internal/ir/notify"
	"nox/internal/ledger/reconcile"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/research/oppmatch"
	"nox/internal/research/scan"
	"nox/internal/review"
	"nox/internal/risk/position"
	"nox/internal/store/pgstore"
	"nox/internal/trading/engine"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	dsn := os.Getenv("DB_DSN") // D-145 引导件：连库前必需，业务配置权威=PG
	if dsn == "" {
		dsn = "postgres://nox:nox@localhost:5433/nox?sslmode=disable"
	}
	// 1. DB 连接 + migrations 自跑（§11.2）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startPprof(ctx) // 本机诊断面（127.0.0.1:6060 / NOX_PPROF_ADDR；不入公网 mux）
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer st.Close()
	stMoney := mustMoneyStore(ctx, dsn) // D-633 拆票：钱径保底池（exec/settle/position 独占——读面爆量不饿钱径）
	defer stMoney.Close()

	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Println("migrations applied")
	reportCheckDrift(ctx, st)                  // 约束漂移启动自检：迁移链 CHECK 终态 vs 活库 pg_get_constraintdef
	sealExchangeKeysOrFatal(ctx, st)           // D-515 S2：密钥明文→enc 自封（MASTER_KEY 缺失且需封 → fatal）
	seedConfigFromEnv(ctx, st)                 // D-515 S4：空表播种 env→库（一次性，幂等）
	secrets := applyAppSecretsOrFatal(ctx, st) // D-515 S3：密键注入 env（须在 config.Load 前；缺 KEY 表非空 → fatal）
	acRows, acErr := st.LoadAppConfig(ctx)
	if acErr != nil {
		log.Fatalf("[config] app_config load: %v (startup fail-closed)", acErr)
	}
	acEnvSnap, acDefaults := config.SnapshotAppConfigEnv(), config.LoadSansAppConfigKeys() // D-523 键视图双快照（须在 env overlay 前，单线程窗）
	// 2. QuoteBus（§4.1）
	qbus := bus.New()
	// 2a. 经济参数（D-035：全部 env 可覆盖，禁散落硬编码魔法数字）
	cfg := config.Load()
	// D-368 S3 + D-515 S4：app_config overlay（DB>env>default）——必须在 engine/组件构造前。
	config.ApplyAppConfigOverlay(cfg, acRows)
	secRes := newSecretResolver(secrets, cfg) // D-545 S5：密钥热读缓存（DB∪env 种子+热闸标量）
	log.Printf("config: cross_cex_bps=%.2f cross_cfd_bps=%.2f funding_min=%.1f%% carry_min=%.1f%% grab_min=%.1f%% slippage=%.1fbps margin=%.1f%% comm_cfd=%.2f comm_cex=%.1f gate_taker=%.1f close_bps=%.1f freshness=%v opp_ttl=%v opp_pub_delta=%.2f swap_refresh=%v trademode_ttl=%v symbols_ttl=%v",
		cfg.CrossVenueMinGrossBpsCEX, cfg.CrossVenueMinGrossBpsCFD, cfg.FundingMinNetAnnualized, cfg.CarryMinNetAnnualized, cfg.GrabMinNetAnnualized,
		cfg.SlippageBps, cfg.MarginRateAnnual, cfg.CommissionCFDBps, cfg.CommissionCEXTakerBps, cfg.CommissionTaker["gate"],
		cfg.CloseBpsThr, cfg.DetectorFreshnessTTL, cfg.OpportunityTTL, cfg.OppPublishDeltaBps, cfg.SwapRefresh, cfg.TradeModeTTL, cfg.SymbolsTTL)
	// D-069：告警暂停开关（demo 阶段避免 LEGGED/对账不一致告警刷屏）
	st.SetAlertPaused(cfg.AlertPaused)
	// D-592 S1：告警同键去重窗（config 层 envDuration 解析，缺省 30min）。
	st.SetAlertDedupWindow(cfg.AlertDedupWindow)
	if cfg.AlertPaused {
		log.Println("config: alert paused (NOX_ALERT_PAUSED=true)")
	}
	assetEquiv := initAssetEquiv(ctx, st) // D-300 裁 5 单实例（engine+detector 共享）
	// 3. 检测器（§5）
	xassetAllow := &xassetAllowRef{} // D-017 R1：detector 白名单前置滤→引擎热重载 holder 延迟绑定（runServices 内 Bind）
	detectors := buildDetectors(cfg, assetEquiv, st, xassetAllow.match)
	// 4. 评估器配置（§12）；5. CEX 行情采集（REST 轮询，§3.3）
	evalCfg := buildEvalConfig(cfg)
	// D-469b S2/S3：双 kick topic（config 变更+positions 变更；helper 内注释详口径）。
	configKick, posChange := newKickTopics()
	// D-508 S9：域数据刷新通知总线（写源 Publish(topic)→StreamUpdates case 14；scan/intra/trx/pf 侧注入）。
	refreshTopic := bus.NewTopic[string]()
	// D-190/D-211：员工花名册——编制入库+safego 心跳/退出钩接 PG（须在首个 worker 前装钩：
	// Beat 仅 fn 启动打一次，钩未装即丢拍→在岗名册显 NULL 离线假象，实证 watchlist-provider）。
	wireEmployeeRoster(ctx, st, refreshTopic)
	cexCfg, cexSymbols := buildCexExchangeConfig(ctx, st, configKick, cfg, secRes) // D-451 S1：装配块提取至 wire_cexscan.go（纯搬家）
	alertCooler := newAlertCooler()                                                // D-301c S2：告警同指纹冷却单实例，四处注入点共享透传
	// 5a. CEX 报价 → QuoteBus（WS+轮询装配已拆 wire_quotes.go，D-366 S2 同 wire_* 先例）
	factCache := engine.NewFactCache()
	// D-604 N3：funding/swap 惰性判龄窗——同源既有 cfg 键；funding 键 env 缺省 0
	// （退回㉔②：缺省 24h 在消费侧装配 fallback——同款复刻禁生产哑火；
	// 复审修件：常量引用替裸字面量，禁双份 24h——wire_scan.go:31 同源）。
	fundingMaxAge := cfg.UniverseFundingMaxAge
	if fundingMaxAge <= 0 {
		fundingMaxAge = scan.UniverseFundingMaxAge
	}
	factCache.SetAgeGates(fundingMaxAge, cfg.MTData.SwapFactMaxAge)
	if isRealMap, err := st.ListVenueIsReal(ctx); err != nil { // D-178：统一 is_real 映射（CFD+CEX），检测层 fail-open
		log.Printf("[main] ListVenueIsReal failed (fail-open): %v", err)
	} else {
		factCache.UpdateIsRealMap(isRealMap)
	}
	// D-509 S1：WS funding fact 发射口接 makeMetaAwareSink（meta 反哺+is_real 复用零新通道）。
	// D-301a S2：fundingInfo 缓存 boot 一次——D-509 S2 起实时流/history/(S5前)REST 三路共用。
	fundingEmit, fundingIntervals := newFundingEmitAndIntervals(ctx, st, factCache, cexCfg)
	gateWs := startQuoteStreams(ctx, cexCfg, qbus, alertCooler, st, fundingEmit, fundingIntervals) // D-570 包C：ref 透传 monitors 装配 OnContractInfo
	// 5b. 轮询 Fact 源（CEX funding + MT swap）→ FactSink → 存储 + FactCache。
	//      Scheduler 在 brokerMgr 创建后启动（mt_swap 源依赖其 SwapFacts 聚合，D-036）。
	cexCollectors := buildCexFundingCollectors(st, cexCfg, alertCooler, universeDepthBases(ctx, st), fundingIntervals, cfg) // D-178 提取至 wiring.go 保 main() ≤300 行；D-057④ 深度基名改接 universe
	// D-469a S4：私有 WS 事件 kick 通道（buffered(1) 非阻塞合并——同 sched.Kick 先例；
	// 事件只改「何时醒」，定时路径保留为断流兜底）。
	incomeKickCh := make(chan struct{}, 1)    // binance FUNDING_FEE → income RunOnce
	okxIncomeKickCh := make(chan struct{}, 1) // OKX balance_and_position funding_fee → okx income
	ioKickCh := make(chan struct{}, 1)        // binance DEPOSIT/WITHDRAW → cex-io RunOnce
	// D-163：探针提案机审 kick 总线（explorer 落库 → dashSvc.RunProbeAdjudicator 订阅端）。
	probeTopic := bus.NewTopic[struct{}]()
	// income/IO 采集器与扫描器批量装配提取（D-474a 保 main() ≤300 行门禁）
	// D-620⑦：exec 注册表+真金单闸单实例（liveGate readiness=readyz Tradable；cp 穿注 dashboard/dciexec/selfwatch——wire_livegate.go）。
	execReg, liveGate, cp := wireExecGateChain(st, qbus, cfg)
	brokerMgr := dashboard.NewBrokerManager(st, qbus, execReg, cfg.SymbolsTTL)
	// D-597 时序审计病灶①：swapledger 三倍日 venue weekday 延迟绑定（独立 cache 实例
	// ——bpmon 自持一枚，fetcher 无状态每实例 24h TTL×3 户重拉量可忽略）。
	swapVenueWeekday.Bind(dashboard.NewServerTZCache(24*time.Hour, serverTZFetcherFor(brokerMgr)))
	// D-604 P1②：swapscan 入榜闸 MT equity 判定拍实拉（依赖注入——research 域禁
	// import dashboard API 面；读败=nil ok=fail-closed 拒入榜非降级陈值）。
	oppmatch.BindMTEquityFn(func(venue string) (float64, bool) {
		// 退回㉖顺件①：5s 界（网关挂起卡死扫描环防护；venueLeverage 4s 限 ctx 先例）。
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		eq, _, _, err := brokerMgr.MTAccountSummary(ctx, venue)
		if err != nil {
			return 0, false
		}
		return eq, true
	})
	startCollectorsAndScanners(ctx, st, cfg, cexCfg, cexSymbols, incomeKickCh, okxIncomeKickCh, ioKickCh, refreshTopic, probeTopic, brokerMgr, cp)
	// 5c. TickSampler（§4.3）采样+落库装配已拆 wire_quotes.go（D-366 S2）。
	startTickSampler(ctx, qbus, st, cexCfg)

	// 6. 执行器注册表（§13.2）+ Broker 管理器（§broker 管理器方案）——引擎与轮询源依赖其解析
	brokerMgr.SetMarginReservePct(cfg.MarginReservePct) // D-415：动态 MT5 executor 资金预检安全垫

	// D-177 报价量级校验：wildcard 订阅，首条报价触发，量级不符自动隔离
	safego.Go("main:quoteGuard", func() { bus.NewQuoteGuard(qbus, st, brokerMgr.ReloadSymbolMap).Run(ctx) })

	// 5b. 轮询 Fact 源启动（CEX funding + MT swap + TRX 质押 + CMC Markets——1048 回归）；D-231 S5 hedgeTopic=对冲刷新总线（TRX 变化→case 12）。
	hedgeTopic := bus.NewTopic[struct{}]()
	trxW := buildTrxCollectors(ctx, st, hedgeTopic, refreshTopic, cfg, secRes)
	var trxSources []collect.Named
	if trxW != nil {
		trxSources = trxW.sources
	}
	cmcSources, cmcWs := buildCmcCollectors(ctx, st, cfg, secRes, qbus)                                  // 凭据缺=内部优雅降级 nil+缺口落箱
	sources := append(append(append([]collect.Named{}, cexCollectors...), trxSources...), cmcSources...) // D-211 TRX + CMC（D-237/D-266 已随 D-139 摘除）
	sources = append(sources,                                                                            // mt_swap 依赖 brokerMgr.SwapFacts（D-036）
		collect.Named{Name: "mt_swap", Interval: cfg.SwapRefresh, Collector: &collect.MTSwapCollector{Fetch: brokerMgr.SwapFacts}},
		collect.Named{Name: "mt_sessions", Interval: 24 * time.Hour, PollTimeout: 120 * time.Second, Collector: &collect.MTSessionsCollector{Fetch: brokerMgr.SessionsFacts}}, // D-029 ①：周表静态每日刷新（首拍立即；brokerMgr 聚合静态适配器源）
		// D-140 S2：MT5 符号元数据采集（CS 自动来源，1h 低频）+ meta sink 反哺
		collect.Named{Name: "mt_meta", Interval: 60 * time.Minute, Collector: &collect.MTSwapCollector{Fetch: brokerMgr.MetaFacts}},
	)
	sched := &collect.Scheduler{
		Sources: sources, Sink: makeMetaAwareSink(st, factCache), PollTimeout: 35 * time.Second,
	}
	// D-469c S3：facts 写后完成回调 → factsDoneTopic（swapArb factsKick 装配在 runServices 消费）。
	factsDoneTopic := bus.NewTopic[string]()
	sched.AfterRun = factsDoneTopic.Publish
	// D-518 S2：WS funding emit → factsDone 桥（REST 退役后唯一 funding 入口；详 wire_quotes.go）。
	wireFundingDoneBridge(factsDoneTopic)
	brokerMgr.SwapKick = func() { sched.Kick("mt_swap") }         // D-060 F2：attach/映射增删 → 立即补一轮 swap（boot 竞态修复）
	brokerMgr.SessionsKick = func() { sched.Kick("mt_sessions") } // D-029 R2：attach/映射增删 → 立即补一轮周表（同盲窗修复）
	// D-099/D-107 P2：margin 事件驱动 sink + 触发账户重采（OnOrderProfit）。先于 attach 设置，nil 安全。
	brokerMgr.MarginSink = func(f fact.Fact) {
		if err := makeFactSink(st, factCache)(context.Background(), []fact.Fact{f}); err != nil {
			log.Printf("margin: sink: %v", err)
		}
		if k := brokerMgr.OrderProfitKickFn(); k != nil {
			k(keydomain.Join(f.Venue, "mt5"))
		} // D-107 P2/D-071：→ acctMon.Kick 定向（fact.Venue=裸账号，补 :mt5 全键）
	}
	orderTopic := bus.NewTopic[bus.OrderEvent]() // D-107 P1：订单事件总线（OnOrderUpdate → P2/P3 订阅，事件只含通知——前端零信任）
	brokerMgr.OrderEventSink = func(e bus.OrderEvent) { orderTopic.Publish(e) }
	// D-114/D-264：告警总线+飞书 hook（warn/critical 推，nil 安全）。
	alertTopic := bus.NewTopic[pgstore.Alert]()
	feishu := notify.NewFeishuClient(cfg.FeishuAppID, cfg.FeishuAppSecret)
	startMacro(ctx, st, secRes, feishu) // D-528 S8：宏观采集层；D-168 ⑤ 日历日报直推恢复
	// D-508 S5：feishu 扇出改 worker 池（非阻塞投递，满丢弃留痕），chatID 60s 缓存。
	feishuPush := wireFeishuPush(st, feishu)
	bridgePush := bridgePushFor(wireBridgePush(ctx, st, secRes)) // §8.1 告警桥 sink 旁路
	wireAlertFanout(ctx, st, alertTopic, feishuPush, bridgePush) // SLO 票⑤：内插 hook+扇出扫带共喂
	// D-115：safego panic 全局告警口——任何协程 panic 落 alerts critical（30min 同键去重），
	// 经 SetAlertHook→feishu 同管道扇出；GoLoop 域附「退避重启」、Go 域附「协程终止」。
	safego.SetOnPanic(func(name string, r any, willRestart bool) {
		state := "协程终止"
		if willRestart {
			state = "协程退避重启"
		}
		actx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := st.InsertAlertDedup(actx, "critical",
			fmt.Sprintf("[safego] %s panic（%s）: %v", name, state, r), 30*time.Minute); err != nil {
			log.Printf("[safego] panic alert insert: %v", err)
		}
	})
	// D-104 P2 U1/U2：网关 failover + 断流分级告警下沉（config → brokerMgr，attach 透传 adapter.SetGateway/AlertSink）。
	brokerMgr.MT5Gateway, brokerMgr.MT5GatewayFallbacks = cfg.MTData.MT5Gateway, cfg.MTData.MT5GatewayFallbacks
	brokerMgr.AlertSink = alertCooler.Wrap1(func(level, msg string) {
		if err := st.InsertAlert(context.Background(), nil, level, msg); err != nil {
			log.Printf("[alert] insert: %v", err)
		}
	})
	// D-269 S4：TRX 投票收益优化器装配（env 未设置私钥 → 优雅降级 nil）。
	trxVoteSched := buildTrxVoteOptimizer(ctx, st, cfg, secRes, brokerMgr.AlertSink) // D-564 S5：凭据走 SecretResolver
	startTrxVoteOptimizer(trxVoteSched, trxW, ctx)
	safego.Go("main:sched", func() {
		if err := sched.Run(ctx); err != nil {
			log.Printf("[collect] scheduler: %v", err)
		}
	})

	// 5d. 检测→评估→存储 引擎（§5/§6/D-026）。oppTopic 新机会站内推送总线（D-050；修复⑭ S3 退役 → 全量事件流）→ case 2。
	oppTopic := bus.NewTopic[pgstore.Opportunity]()
	commResolver := commissionResolver(cfg, brokerMgr) // D-111：engine + dashboard 共用
	eng := engine.New(qbus, detectors, evalCfg, st, factCache, commResolver, cfg.OppPublishDeltaBps, oppTopic, cfg.StoreMinNetBps, cfg.FundingHedgeMinNetStore, cfg.PerpFundingMinNetStore, cfg.CarryMinNetStore, cfg.XassetCarryMinNetStore)
	eng.SetAssetEquiv(assetEquiv)                            // D-300 G1 快照扩展（与 detector 同实例）
	wireCexPaperStep(ctx, st, eng, refreshTopic, cfg)        // D-555 S1：双CEX 纸面轨挂 FUNDING_HEDGE 候选
	eng.SetExcludeCanonicals(cfg.AutoTradeExcludeCanonicals) // D-072：排除 BRENT/WTI（执行层+检测层双用途）
	// §1 自动淘汰闭环：disabled 策略产出不入库（系统已自证亏损，展示假机会误导）。
	startDisabledStrategiesListener(ctx, st, eng)
	safego.Go("main:eng", func() { eng.Run(ctx) })
	// 5e. 持仓监控（§13.4）+ 个人持仓价格阈值监控（财务线 TRX 持仓接入，D-046）
	posMon := position.New(qbus, stMoney, cfg.CloseBpsThr, cfg.AlertResendInterval)
	persMon := position.NewPersonalMonitor(qbus, st) // D-063：现价广播面拆（渲染退役），仅保留阈值告警
	wirePositionKicks(st, posMon, posChange)         // D-469b S3
	safego.Go("main:posMon", func() { posMon.Run(ctx) })
	safego.Go("main:persMon", func() { persMon.Run(ctx) })

	// 6b. 对账器（§13.4 B2）：querier 集合由 Broker 管理器动态刷新
	// D-073+ 对账自动收尾：Closer/StatUpdater 在 dashSvc 创建后注入；OrderHistoryQueriers 经 D-416 镜像钩子驱动
	recon := reconcile.New(st)
	pv := newPipelineVerifier() // D-108 P1 R1：PositionVerifier 生产装配（适配 venue.Querier→exec.PositionVerifier）
	brokerMgr.SetQuerierHook(func(qs []venue.Querier) { recon.SetQueriers(qs...); pv.set(qs) })
	// D-416：历史查证器镜像钩子——DB 账户 attach/detach 同步注册到对账器。
	brokerMgr.SetHistQuerierHook(func(qs []reconcile.OrderHistoryQuerier) { recon.SetOrderHistoryQueriers(qs...) })

	// 6c. MT5 环境变量适配器（静态，querier 并入 Broker 管理器）
	setupMTAdapters(ctx, execReg, brokerMgr, qbus, cfg.MarginReservePct, st)

	// 6d. DB 中已配置账号的动态适配器恢复（boot）+ D-140 S1 规则库自动补录（best-effort）
	// D-142 ②：AddBroker 后异步触发规则库补录（免重启），复用 backfillVenueRules。
	ruleEng := normalize.NewRuleEngine()
	brokerMgr.RuleBackfill = func(ctx context.Context, venue string) {
		backfillVenueRules(ctx, st, brokerMgr, venue, ruleEng)
	}
	safego.GoPlain("main:brokerMgr.StartAll", func() { brokerMgr.StartAll(ctx) })
	safego.GoPlain("main:ruleBackfill", func() { runRuleBackfill(ctx, st, brokerMgr) })

	// 7. 执行管线（§13.1）—— D-072 同源：env 驱动，不再硬编码
	pipeline := newExecPipeline(execReg, stMoney, qbus, cfg, pv)

	// 8. Dashboard RPC（§8.2）
	// 复盘触发闭包（§13.6，D-034）：平仓后 fire-and-forget 跑统计复盘；boot 也触发一次补齐存量
	reviewCfg := review.Config{
		MinSamples:         cfg.ReviewMinSamples,
		CrossBiasBps:       cfg.ReviewCrossBiasBps,
		CarryBiasAnnualPct: cfg.ReviewCarryBiasAnnualPct,
	}
	reviewRun := func() {
		if err := review.Run(context.Background(), st, reviewCfg); err != nil {
			log.Printf("[review] %v", err)
		}
	}
	safego.Go("main:reviewRun", func() { reviewRun() }) // boot 触发（存量已平仓持仓）

	// 6d~6g 监控器 + dashboard 服务 + auth + runServices 装配提取至 main_monitors.go 保 main() ≤300 行。
	setupMonitorsAndServices(monitorsSetupArgs{
		cexCfg: cexCfg, liveGate: liveGate, cp: cp,
		ctx: ctx, cancel: cancel, cfg: cfg, st: st, stMoney: stMoney, qbus: qbus, execReg: execReg, acDefaults: acDefaults, acEnvSnap: acEnvSnap, secRes: secRes,
		brokerMgr: brokerMgr, pipeline: pipeline, recon: recon, factCache: factCache,
		oppTopic: oppTopic, orderTopic: orderTopic,
		alertTopic: alertTopic, hedgeTopic: hedgeTopic, refreshTopic: refreshTopic, reviewRun: reviewRun,
		commResolver: commResolver, dsn: dsn, alertCooler: alertCooler, // D-301c S2：冷却收口 cexAlertFn 用
		kickFn:       func(name string) { sched.Kick(name) },                             // D-249 S3：dashboard.TrxVoteAprRefresh on-open 触发采集补轮
		feishuClient: feishu,                                                             // D-270 S3：webhook 回复解析需持有 FeishuClient
		incomeKickCh: incomeKickCh, okxIncomeKickCh: okxIncomeKickCh, ioKickCh: ioKickCh, // D-469a S4
		factsDone:  factsDoneTopic,                   // D-469c S3
		configKick: configKick, posChange: posChange, // D-469b S2（D-063：persMon 渲染回推面拆）
		probeTopic:  probeTopic,                // D-163：探针提案机审 kick
		gateWS:      gateWs,                    // D-570 包C S6⑤：OnContractInfo 装配寻址
		xassetAllow: xassetAllow, cmcWs: cmcWs, // D-017 R1+D-212：detector Allow 延迟绑定；cmc ws LiveState/健康注入
	})
}
