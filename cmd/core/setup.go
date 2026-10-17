package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/adapter"
	"nox/internal/data/bus"
	"nox/internal/data/fact"
	"nox/internal/data/keydomain"
	"nox/internal/ir/notify"
	"nox/internal/ledger/reconcile"
	"nox/internal/ledger/settlement"
	"nox/internal/ledger/swapledger"
	"nox/internal/platform/config"
	"nox/internal/platform/safego"
	"nox/internal/portfolio/budget"
	"nox/internal/portfolio/lifecycle"
	"nox/internal/research/oppmatch"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
	"nox/internal/trading/autotrade"

	"nox/internal/trading/exec"
)

// setupMTAdapters 装配 MT5 静态适配器（§6c）——querier 并入 Broker 管理器。
// 历史查证器经 AddStaticHistoryQuerier 走同一聚合钩子（D-416：与 DB 动态账户同源）。
// D-162R4：本径=env 死装配（MT5_* 引导件存量兼容面）——生产权威装配=DB 驱动
// startMT5（broker_symbol_map/broker_accounts）；本径改动只保编译不增能力，
// 复合报价钟等能力注入以 DB 径为准（两径同款 CompositeTickAnchor 已镜像，勿再扩）。
func setupMTAdapters(ctx context.Context, execReg *exec.Registry, brokerMgr *dashboard.BrokerManager, qbus *bus.QuoteBus, marginReservePct float64, st *pgstore.Store) {
	mt5Symbols := brokerSymbols("MT5")
	if mt5Cfg := loadMT5Config(); mt5Cfg != nil {
		mt5a := adapter.NewMT5Adapter(mt5Cfg.venue, mt5Cfg.host, mt5Cfg.server, mt5Cfg.port, mt5Cfg.user, mt5Cfg.pass)
		mt5a.SetSymbolMap(symbolMap(mt5Symbols))
		if err := mt5a.Connect(ctx); err != nil {
			log.Printf("[mt5:%s] connect: %v", mt5Cfg.venue, err)
		} else {
			ch, cancelSub := mt5a.Subscribe(mt5Symbols)
			safego.GoLoop("setup:mt5-quote-fwd", func() {
				for q := range ch {
					qbus.Publish(q)
				}
			})
			_ = cancelSub
			mt5Exe := exec.NewMT5Executor(mt5Cfg.venue, mt5a.Token(), mt5a.TradingClient(), mt5a.MT5Client())
			mt5Exe.SetMarginReservePct(marginReservePct)
			execReg.Register(mt5Exe)
			// D-162R2/R3 复合报价钟：order 原生 symbol → lastQuote→ticks 两级；
			// miss → broker_symbol_map 解 canonical（XAUUSDm→XAUUSD 锚中 feed 键——
			// ticks 落库键=发布侧 canonical）→ 同两级；解不出保留原值不猜。
			symCanon := map[string]string{}
			if maps, err := st.ListSymbolMappings(ctx, mt5Cfg.venue); err == nil {
				for _, m := range maps {
					symCanon[m.BrokerSymbol] = m.Canonical
				}
			} else {
				log.Printf("[mt5:%s] symbol mappings load: %v（canonical 锚降级空态）", mt5Cfg.venue, err)
			}
			mt5Anchor := adapter.CompositeTickAnchor(mt5a.LastTickAnchor,
				func(ctx context.Context, sym string) (float64, float64, time.Time, bool) {
					t, ok, err := st.LatestTick(ctx, mt5Cfg.venue, "mt5", sym)
					if err != nil || !ok {
						return 0, 0, time.Time{}, false
					}
					return t.Bid, t.Ask, t.Ts, true
				}, symCanon)
			mt5a.SetPriceAnchor(mt5Anchor)
			mt5Q := reconcile.NewMT5Querier(mt5Cfg.venue, mt5a.Token(), mt5a.MT5Client())
			mt5Q.SetPriceAnchor(mt5Anchor)
			brokerMgr.AddStaticQuerier(mt5Cfg.venue, mt5Q)
			brokerMgr.AddStaticSwapSource(mt5Cfg.venue, func(ctx context.Context) ([]fact.Fact, error) { return mt5a.SwapFacts(ctx, qbus) })
			brokerMgr.AddStaticSessionsSource(mt5Cfg.venue, mt5a.SessionsFacts) // D-029 ①：周会话表采集源
			brokerMgr.AddStaticHistoryQuerier(mt5Cfg.venue, reconcile.NewMT5OrderHistoryQuerier(mt5Cfg.venue, mt5a.Token(), mt5a.MT5Client()))
			log.Printf("[mt5:%s] adapter ready", mt5Cfg.venue)
		}
	}
}

// readyzCheck 已提取至 setup_readyz.go（D-182 S3 拆分）。

// runServices 启动自动交易引擎 + swap 记账 + HTTP 服务，并阻塞至收到终止信号后优雅关闭。
func runServices(ctx context.Context, cancel context.CancelFunc, cfg *config.Config, st *pgstore.Store, dashSvc *dashboard.Service, oppTopic *bus.Topic[pgstore.Opportunity], orderTopic *bus.Topic[bus.OrderEvent], qbus *bus.QuoteBus, factsDone *bus.Topic[string], dsn, path string, handler http.Handler, settleDeps settlement.Deps, settlementCollector *dashboard.SettlementProfileCollector, execReg *exec.Registry, liveGateChecker *gate.LiveGateChecker, feishuClient *notify.FeishuClient, secRes *config.SecretResolver, configKick *bus.Topic[struct{}], refreshTopic *bus.Topic[string], pipeline *exec.Pipeline, xassetAllowBind func(*autotrade.Engine)) {
	// 8b. 自动交易引擎（D-070）：订阅机会 → 风控 → 自动执行 → 自动平仓 → 策略淘汰
	allowVenues, allowCanonicals := allowRulesFromTable(ctx, st, cfg.Environment) // D-443 S3：白名单表化（env 弃用；live 空 venue 集 fatal）
	seedSwapArbCombosFromEnv(ctx, st, cfg)                                        // D-515 S5：表空+env 有值一次性播种（幂等）
	saAllow, saDeny := swapArbAllowFromTable(ctx, st)                             // D-515 S5：组合门禁表化（env 停读）

	atEngine := autotrade.New(gate.Config{
		Enabled:                   cfg.AutoTradeEnabled,
		MaxNotional:               cfg.AutoTradeMaxNotional,
		MaxPositions:              cfg.AutoTradeMaxPositions,
		MaxDailyLoss:              cfg.AutoTradeMaxDailyLoss,
		MinNetBps:                 cfg.AutoTradeMinNetBps,
		CloseBpsThr:               cfg.CloseBpsThr,
		PollInterval:              cfg.AutoTradePollInterval,
		NotionalUSD:               cfg.OpportunityNotionalUSD,
		AllowVenues:               allowVenues,     // D-443 S3：表加载初值（热读 holder 续命）
		AllowCanonicals:           allowCanonicals, // D-443 S3：同上
		ExcludeCanonicals:         cfg.AutoTradeExcludeCanonicals,
		SwapArbAllowCombos:        saAllow,                                        // D-515 S5：表化（env 停读，播种迁移桥在上方）
		XAssetAllowCombos:         xassetAllowFromTable(ctx, st, cfg.Environment), // D-338 S3：白名单表化（env 弃用；fail-closed fatal）
		MtQuoteStaleMin:           cfg.MtQuoteStaleMin,                            // D-029 ②：MT 报价新鲜度阈值（app_config 置值启用动态径）
		MacroBlackoutMin:          cfg.MacroBlackoutMin,                           // D-053 ③：high 事件禁区窗 ±分钟（缺省 15）
		MacroBlackoutCurrencies:   cfg.MacroBlackoutCurrencies,                    // D-053 ③：禁区币种集（缺省 USD）
		SwapArbDenyCombos:         saDeny,                                         // D-515 S5：deny 同表双轨
		SwapArbAutoAllow:          cfg.SwapArbAutoAllow,                           // D-168 自动准入
		SwapArbMaxGrossExposure:   cfg.SwapArbMaxGrossExposure,                    // D-168 全局额度
		MaxActiveCombos:           cfg.SwapArbMaxActiveCombos,                     // D-138 单组合口径
		Environment:               cfg.Environment,                                // D-159 W1a I6：environment/is_real 检查
		LiveTrade:                 cfg.LiveTrade,                                  // D-159 W6a S2：LIVE 人工开关
		LiveGateChecker:           liveGateChecker,                                // D-195-C1：LIVE 模式导出门
		SwapDeviationGatePct:      cfg.SwapDeviationGatePct,                       // D-165 S1：swap 偏差拦截
		SwapDeviationExitPct:      cfg.SwapDeviationExitPct,                       // D-165 S2：swap 偏差退出
		SwapFactsZeroCheckEnabled: cfg.SwapFactsZeroCheckEnabled,                  // D-165 S3：swap facts 零值校验
		LegContractSizeResolver:   dashSvc.LegContractSize(),                      // D-165.1 S1：MT 腿原生 CS（PriceActual 链）
		LegUnitQtyResolver:        dashSvc.LegUnitQty(),                           // D-594：canonical 单位数/手（canonical 报价链）
		Quotes:                    qbus,                                           // D-190：偏差 gate usdnotional.PerLot JPY 换算
		MeanRevertThresh:          cfg.MeanRevertThresholds,
		MinNetSwapBps:             cfg.AutoTradeMinNetSwapBps,
		SwapArbExitBps:            cfg.SwapArbExitNetBps,
		FundingExitPct:            cfg.FundingExitNetPct,
		CarryMinNetAnnualized:     cfg.CarryMinNetAnnualized,    // D-187：CARRY 合并 SWAPARB 统一门禁栈
		PerpFundMinHold:           cfg.PerpFundMinHold,          // D-133 T1 持有下限
		PerpFundExitDebounce:      cfg.PerpFundExitDebounce,     // D-133 T1 连续去抖
		EntrySpreadMinBps:         cfg.SwapArbEntrySpreadMinBps, // D-077 修复⑪B N6
		SwapArbMaxLegSpreadBps:    cfg.SwapArbMaxLegSpreadBps,   // D-159 W4a R7 skew 门禁
		EntrySpreadMaxBps:         cfg.SwapArbEntrySpreadMaxBps, // D-169 P0
		EntryBasisMaxBps:          cfg.SwapArbEntryBasisMaxBps,  // D-169 P0
		BasisStopBps:              cfg.SwapArbBasisStopBps,      // D-180 S3
		// D-328 S5：carry 入口 funding 持久性闸（单行保 setup.go ≤300 行门禁）
		CarryRegime:              exec.CarryRegimeConfig{Window: time.Duration(cfg.CarryRegimeWindowH * float64(time.Hour)), FloorPct: cfg.CarryRegimeFloorPct, MinSamples: cfg.CarryRegimeMinSamples, PulsedAware: pulsedAwareFor(cfg.Environment)}, // D-334c S1
		MinNetCarryBps:           cfg.SwapArbMinNetCarryBps,                                                                                                                                                                                          // D-180 S4
		SpecDiscount:             cfg.SwapArbSpecDiscount,                                                                                                                                                                                            // D-180 S4
		MaxPaybackDays:           cfg.SwapArbMaxPaybackDays,                                                                                                                                                                                          // D-180 S4
		MaxLegsPerSymbol:         cfg.SwapArbMaxLegsPerSymbol,                                                                                                                                                                                        // D-180 S4
		FreshnessTTL:             cfg.DetectorFreshnessTTL,                                                                                                                                                                                           // D-077 修复⑪B N6
		SwapFactMaxAge:           cfg.MTData.SwapFactMaxAge,                                                                                                                                                                                          // D-104 P0 fail-closed 门禁
		ReentryCooldownDays:      cfg.SwapArbReentryCooldownDays,
		StopReentryCooldown:      time.Duration(cfg.StopReentryMinutes) * time.Minute, // D-372 gate 止损重进冷却
		XAssetExecDriftBps:       cfg.XAssetExecDriftBps,                              // D-378a S2 执行质量闸
		PositionFallbackInterval: time.Duration(cfg.PositionEvalSec) * time.Second,    // D-378a S3 评估节拍
		ComboLossLimit:           cfg.ComboLossLimit,                                  // D-378a S4 组合保险丝
		ComboLossWindow:          time.Duration(cfg.ComboLossWindowH) * time.Hour,     // D-378a S4 保险丝窗口
		XAssetDisasterBps:        cfg.XAssetDisasterBps,                               // D-378b 灾难带
		XAssetDisasterNoiseMult:  cfg.XAssetDisasterNoiseMult,                         // D-378b 灾难带 noise 倍数
		XAssetDisasterDebounce:   cfg.XAssetDisasterDebounce,                          // D-378b 灾难带去抖
		XAssetQuoteMaxAge:        cfg.XAssetQuoteMaxAge,                               // D-378b 报价新鲜度
		XAssetFastMoveBps:        cfg.XAssetFastMoveBps,                               // D-111 快行情挂起闸
		XAssetFastMoveWindow:     cfg.XAssetFastMoveWindow,                            // D-111
		XAssetCexFeeBps:          cfg.XAssetCexFeeBps,                                 // D-409 费用估算率（enrich 同源）
		// D-014 S2：econ 费率源——MT 腿 broker_accounts.commission_bps（venue 事实）、
		// CEX 腿 exchange_configs.taker_fee_bps（venue+type 配置）；缺=ok=false fail-closed。
		EconFeeBps: func(ctx context.Context, venueKey string) (float64, bool) {
			bare := keydomain.BareOf(venueKey)
			if keydomain.IsMTKey(venueKey) {
				v, ok, err := st.BrokerCommissionBps(ctx, bare)
				return v, err == nil && ok
			}
			v, ok, err := st.ExchangeTakerFeeBps(ctx, bare, keydomain.TypeOf(venueKey))
			return v, err == nil && ok
		},
		// D-018 后段①：maker 费率源——exchange_configs.maker_fee_bps（MT 腿无 maker 语义→false）。
		EconMakerFeeBps: func(ctx context.Context, venueKey string) (float64, bool) {
			if keydomain.IsMTKey(venueKey) {
				return 0, false
			}
			v, ok, err := st.ExchangeMakerFeeBps(ctx, keydomain.BareOf(venueKey), keydomain.TypeOf(venueKey))
			return v, err == nil && ok
		},
		MaxBreakevenDays:           cfg.MaxBreakevenDays,
		HarvestHorizonDays:         cfg.HarvestHorizonDays,
		RiskDD30USD:                cfg.RiskDD30USD,                // D-032 ①：30d 账本净额停机线
		RiskIncidentAckID:          cfg.RiskIncidentAckID,          // D-032 ②：事故仓 ack 游标（env 兜底）
		RiskCexEquityPct:           cfg.RiskCexEquityPct,           // D-032 ③：CEX 权益纪律（%）
		RiskCfdEquityPct:           cfg.RiskCfdEquityPct,           // D-032 ③：MT 权益纪律（%）
		RotateMaxPerDay:            cfg.RotateMaxPerDay,            // D-406 轮转日限
		SwapArbMarginBudgetPct:     cfg.SwapArbMarginBudgetPct,     // D-142 ④ 观察版告警
		ProbeEnabled:               cfg.ProbeEnabled,               // D-181 S2 probe 探测仓总开关
		ProbeEvalDays:              cfg.ProbeEvalDays,              // D-181 S2 probe 评估等待天数
		SwapNonSettleVenues:        cfg.SwapNonSettleVenues,        // D-256-A S1：non-settle venue guard
		SwapArbComboLossLimit:      cfg.SwapArbComboLossLimit,      // D-256-A S4：组合级止损计数 limit
		SwapArbComboLossWindowDays: cfg.SwapArbComboLossWindowDays, // D-256-A S4：止损计数回看窗口
		SwapArbRevivalBps:          cfg.SwapArbRevivalBps,          // D-256-A S5：复活扳机告警阈值
	}, st, dashSvc, oppTopic, qbus)
	// D-087 §3.2：注入 settlement.Settle 依赖 + 淘汰判定函数 + 风险族解析器（D-256-A S2）
	atEngine.SetSettleDeps(settleDeps, newDBEliminator(st), lifecycle.FamilyResolverAdapter, newElimHearing(st, cfg))
	// D-004 P1#2：平仓可平性闸 MT5 trade_mode 即时探针——经 execReg 取 TradeModeProber executor（SymbolParams 直查非缓存）。
	atEngine.SetTradeModeProberResolver(func(vk string) exec.TradeModeProber {
		if exe, ok := execReg.Get(vk); ok {
			if prober, ok := exe.(exec.TradeModeProber); ok {
				return prober
			}
		}
		return nil
	})
	// D-556：账号别名闭包注入 gate 资金拒单白话文案（一把启动加载；失败 best-effort 回落 bare 键）。
	if aliasMap, aerr := st.ListAccountAliases(ctx); aerr == nil {
		atEngine.SetGateVenueAlias(func(vk string) string { return aliasMap[vk] })
	} else {
		log.Printf("[wire] load account aliases for gate margin alerts: %v（别名回落 bare 键）", aerr)
	}
	// D-182 S1：注入零结算检测器（scanAndClose 周期对活跃仓检测恒零 → settled=0）
	atEngine.SetZeroSettlementDetector(settlementCollector)
	// D-017 R1：detector 白名单前置滤接通（延迟绑定收口——本行起 Allow=引擎热重载 holder；Reload 后下帧即反映）。
	if xassetAllowBind != nil {
		xassetAllowBind(atEngine)
	}
	// D-140 S2：CS-miss 聚合告警 sink（info 级，同 brokerMgr.AlertSink 同模式）
	atEngine.AlertSink = func(level, msg string) {
		if err := st.InsertAlert(context.Background(), nil, level, msg); err != nil {
			log.Printf("[cs-miss] alert insert: %v", err)
		}
	}
	// D-140 S3：swap 组合列表注入（basis_bps 只展示不动排序，避免 dashboard→autotrade 依赖环）
	dashSvc.SetCarryRegime(exec.CarryRegimeConfig{Window: time.Duration(cfg.CarryRegimeWindowH * float64(time.Hour)), FloorPct: cfg.CarryRegimeFloorPct, MinSamples: cfg.CarryRegimeMinSamples, PulsedAware: pulsedAwareFor(cfg.Environment)}) // D-328 S5 手动同源闸；D-334c S1 两装配点同注
	dashSvc.SetSwapComboLister(func() []dashboard.SwapComboEntry {
		combos := atEngine.DiscoverSwapCombosPublic(context.Background())
		out := make([]dashboard.SwapComboEntry, 0, len(combos))
		for _, c := range combos {
			out = append(out, dashboard.SwapComboEntry{
				Canonical:  c.Canonical,
				VenueLong:  c.VenueLong,
				VenueShort: c.VenueShort,
				NetSwapBps: c.NetSwapBps,
				BasisBps:   c.BasisBps,
				AliasLong:  c.AliasLong,  // D-144 ③
				AliasShort: c.AliasShort, // D-144 ③
			})
		}
		return out
	})
	dashSvc.SetXAssetCarryLive(atEngine.XAssetNetCarryPublic)                                                      // D-340R1 S1：装配补线（复审定案零调用方=live 全量缺省 R1-class 第 4 现）
	dashSvc.SetEconSources(atEngine.PositionEconPublic, atEngine.EntryBreakevenDaysPublic)                         // D-014 ④：回本列/pending_entries 与闸同源
	dashSvc.SetXAssetReloader(atEngine)                                                                            // D-403 S3：毕业链 approve 依赖接线（热重载 nil=fail-closed）
	dashSvc.SetRejectBreaker(atEngine)                                                                             // D-560 S3：熔断快照透出+ResumeXAssetCombo 行为
	oppmatch.GradDelegateReload = func(ctx context.Context) error { return atEngine.ReloadXAssetAllowCombos(ctx) } // D-513 委任批热载钩（nil=静默跳过）
	atEngine.RiskGate().SetBudgetChecker(budget.NewChecker(st))                                                    // D-264 M4.4④ 预算闸真实现（行缺/读败 fail-closed）
	dashSvc.SetRefreshTopic(refreshTopic)                                                                          // D-508 S9：域刷新通知注入（→case 14）
	// D-443 S3/S4：venue gap 报告改表活读（60s 热重载后 ≤60s 反映实时态，非启动快照）。
	dashSvc.SetAllowVenuesSource(func(ctx context.Context) (map[string]bool, error) {
		v, _, err := st.LoadAutotradeAllowRules(ctx, cfg.Environment == "demo")
		return v, err
	})
	// D-475a S6：旗标执行投影 boot 首载——ListActiveFlags 败 fatal（fail-closed 同 allowRules seam；其后 kick/ticker/写旗三触发点续命）。
	if err := atEngine.ReloadFlags(ctx); err != nil {
		allowRulesFatal("[flags] initial load: %v (startup fail-closed)", err)
	}
	safego.GoPlain("setup:atEngine.Run", func() { atEngine.Run(ctx) })
	allowRulesKick, _ := configKick.Subscribe()
	startAllowRulesReloader(ctx, atEngine, allowRulesKick, func(allow []exec.ComboAllowSpec) { // D-443 S3+D-469b S5+D-515 S5：kick 主+5min 兜底
		pipeline.ReloadSwapArbComboAllow(allow)
		dashSvc.SetSwapArbComboAllow(allow)
	})
	// D-180 S3：存量活跃仓 entry 基准回填（boot 一次性，幂等——已有值不覆写）。
	safego.GoPlain("setup:backfillEntrySpread", func() {
		atEngine.BackfillEntrySpreadPublic(ctx)
	})
	// D-107 P4：orderTopic 事件 → atEngine.Kick（OnOrderUpdate 持仓变更 → 触发重扫，scanIfDue 30s 节流）。
	safego.GoLoop("setup:atEngine.orderKick", func() {
		evCh, evCancel := orderTopic.Subscribe()
		defer evCancel()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-evCh:
				if !ok {
					return
				}
				atEngine.Kick()
				atEngine.KickSwapArb() // D-004 P1#5：订单事件=容量/冷却集变化 → swapArb 全扫重建 armed
			}
		}
	})

	wireSwapArbFactsKick(ctx, factsDone, atEngine) // D-469c S3：facts 写后 → swapArb factsKick

	wireWatchEngine(ctx, configKick, st, qbus, cfg.DetectorFreshnessTTL) // 业主视窗 B：盯盘引擎
	// 8c. swap 逐日记账（D-077 修复⑨ V1）：日频 ticker，公式记账 → position_cashflows。
	// ticker 独立日频（per-call 超时+就绪降级——loop-initial-block 铁律）；TripleDayResolver 读 facts 表（V2 顺带产出）。
	swapLedger := swapledger.New(st, dashSvc, swapledger.NewFactTripleDayResolver(st), cfg.SwapNonSettleVenues, cfg.MTData.SwapFactMaxAge, swapVenueWeekday.Func()) // D-597：三倍日判定按 venue server 日期
	safego.Go("setup:swapLedger", func() { swapLedger.StartLoop(ctx) })
	wireSwapReconciler(ctx, st, cfg, dashSvc)

	// 9. HTTP 服务（装配+阻塞+优雅关闭提取至 setup_http.go 保行数）。
	// D-191：DCI 申购执行器（凭据 exchange_configs 驱动，无凭据由 gap 径回执）。
	dciEx := buildDCIExecutor(ctx, st)
	startDCIPoolReconcile(ctx, dciEx)
	startDCIResume(ctx, dciEx)               // D-198：提案断点续执（首个 DCI 采集拍后一次）
	startDCISettle(ctx, dciEx, refreshTopic) // D-207：交割观察（cexscan 拍驱动）
	serveHTTP(cancel, cfg, st, qbus, execReg, liveGateChecker, feishuClient, secRes, dsn, path, handler, probeDecider{svc: dashSvc}, dciEx)
}

// wireExecInjections 装配 exec 注入（修复⑫：⑪A M1 PnL 校准 + CS 复用 + D-105 fx 换算 + D-159 W1a R1 组合门禁）。
// PositionVerifier 已在 pipeline 创建后接线（D-108 P1 R1 pv），此处不重复。
