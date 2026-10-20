package main

// wire_dashboard.go dashSvc setter 注入段（从 main.go 提取以保 main() ≤300 行，D-077 修复⑩ G7）。
// 含：fundingNeedTopic/tradeModes/legCS/maxImbalance/legMinLots/legVolumeStep/oppCS/frontConfig/analyst。

import (
	"context"
	"log"
	"strings"
	"time"

	"nox/internal/api/auth"
	"nox/internal/api/dashboard"
	"nox/internal/data/bus"
	"nox/internal/data/keydomain"
	"nox/internal/ir/notify"
	"nox/internal/platform/analyst"
	"nox/internal/platform/config"
	"nox/internal/platform/httpx"
	"nox/internal/platform/safego"
	"nox/internal/portfolio/budget"
	"nox/internal/research/explorer"
	"nox/internal/research/oppmatch"
	"nox/internal/review/deptreport"
	"nox/internal/review/kbreport"
	"nox/internal/store/pgstore"
	"nox/internal/trading/engine"
	"nox/internal/trading/exec"
)

// wireAuthIntercept D-095：auth 令牌迁 PG 热读装配（自 main_monitors.go 迁出保行数）——
// 有效令牌=secRes（app_secrets DB 值∪env 回落，DB>env）；双空 fatal（D-159 fail-closed，
// allowEmpty 逃生门语义保真）；拦截器每请求经闭包热读（面板轮换写时同步 Set 即效）。
func wireAuthIntercept(secRes *config.SecretResolver, cfg *config.Config) *auth.Interceptor {
	if err := auth.ValidateStartupToken(secRes.Get("NOX_AUTH_TOKEN"), cfg.AllowEmptyAuth); err != nil {
		log.Fatalf("[auth] startup token validation failed: %v", err)
	}
	return auth.NewInterceptor(func() string { return secRes.Get("NOX_AUTH_TOKEN") }).
		SetTOTP(func() string { return secRes.Get("NOX_TOTP_SECRET") }) // TOTP session 支
}

// wireDashboardSetters 注入 dashSvc 的 setter 依赖（leg 查询/CS/frontConfig/analyst）。
func wireDashboardSetters(ctx context.Context, dashSvc *dashboard.Service, cfg *config.Config, st *pgstore.Store, brokerMgr *dashboard.BrokerManager, factCache *engine.FactCache, feishu *notify.FeishuClient, qbus *bus.QuoteBus) {
	fundingNeedTopic := bus.NewTopic[dashboard.FundingNeedNotice]()
	dashSvc.SetFundingNeedTopic(fundingNeedTopic)
	// D-060 F5：手动预检单腿 trade_mode——D-004 P1#2 删 FactCache.TradeMode 单查法后，
	// 经 TradeModes() 只读快照取值（P1#8b：TTL 过滤退役，权威在 exec 即时探针；
	// 执行路径判据已改 exec.TradeModeProber 即时探针，不走此缝）。
	dashSvc.SetTradeModes(func(canonical, venueKey string) (int64, bool) {
		mode, ok := factCache.TradeModes()[canonical][venueKey]
		return mode, ok
	})
	dashSvc.SetLegContractSize(legContractSizeResolver(st)) // D-060 实施修正②：MT 腿原生 CS（PriceActual 链）
	mtWatch := &mtRPCFaceWatch{}
	dashSvc.SetLegUnitQty(legUnitQtyResolver(st, mtWatch))                 // D-594：MT 腿 canonical 单位数/手（canonical 报价链）
	safego.GoLoop("main:mt-rpc-watch", func() { mtWatchRun(st, mtWatch) }) // D-636：RPC 面看门狗
	dashSvc.SetPrimarySymbol(primarySymbolResolver(st))                    // D-383 A2.4：执行解析 primary-only 闸（歧义 fail-closed）
	dashSvc.SetCexContractSize(cexContractSizeResolver(st))                // D-311 S3：CEX perp 腿 ct_val 查 fin_cex_universe
	dashSvc.SetMaxLegImbalancePct(cfg.MaxLegImbalancePct)
	dashSvc.SetFreezeGuard(cfg.FreezeGuard)                                     // D-155：冻结腿预检窗口（≤0 禁用）
	dashSvc.SetSwapArbComboAllow(swapArbAllowFromTableOnly(ctx, st))            // D-515 S5：表化同源热读
	dashSvc.SetXAssetComboAllow(xassetAllowFromTable(ctx, st, cfg.Environment)) // D-338 S3：表化同源（设计自审③漏此位点，施工按同源意图补齐防 auto/manual 分裂）
	dashSvc.SetXAssetAllowEnvironment(cfg.Environment)                          // D-403 R2：approve 扇出 rows 单查环境
	dashSvc.SetEnvironment(cfg.Environment)                                     // D-159 W1a I6：手动入口同源
	dashSvc.SetSwapDeviationGatePct(cfg.SwapDeviationGatePct)                   // D-182 S4：swap 偏差拦截手动入口同源
	dashSvc.SetSwapFactMaxAge(cfg.MTData.SwapFactMaxAge)                        // D-604 N1：deviation 闸 facts 龄期（discover 同源键）
	oppmatch.SetDelegateTickMaxAge(cfg.LegTickWindow)                           // D-604 N2：delegate 真金 sizing 判价窗（残腿窗同源）
	// D-633：delegate 真金 sizing 判价现态源=qbus（DB LatestTickMidWithin 退役锚）
	oppmatch.SetDelegatePriceNow(func(venue, venueType, symbol string) (float64, float64, time.Time, bool) {
		q, ok := qbus.LatestLeg(symbol, symbol, keydomain.Join(venue, venueType))
		return q.Bid, q.Ask, q.Time, ok
	})
	dashSvc.SetLegMinLots(func(venueKey, venueSymbol string) (float64, bool) { // D-061 量规格预检
		venue := keydomain.BareOf(venueKey)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		min, known, err := brokerMgr.BrokerMinLots(ctx, venue, venueSymbol)
		if err != nil {
			log.Printf("[exec] leg min lots %s %s: %v", venue, venueSymbol, err)
			return 0, false
		}
		return min, known
	})
	dashSvc.SetLegVolumeStep(func(venueKey, venueSymbol string) (float64, bool) { // D-070 qty 精度取整
		venue := keydomain.BareOf(venueKey)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		step, known, err := brokerMgr.BrokerVolumeStep(ctx, venue, venueSymbol)
		if err != nil {
			log.Printf("[exec] leg volume step %s %s: %v", venue, venueSymbol, err)
			return 0, false
		}
		return step, known
	})
	dashSvc.SetOppContractSize(oppContractSizeResolver(st)) // D-062：机会卡 USD 派生 CS 查 broker_symbol_map（venue_buy 侧）
	// D-175：启动时加载 venue→alias 缓存注入 Service（best-effort，查询失败 → 空 map，alias 留空前端回退 venueKey）。
	if aliasMap, err := st.ListAccountAliases(context.Background()); err != nil {
		log.Printf("[wire] load account aliases: %v (oppToProto alias 留空)", err)
	} else {
		dashSvc.SetAliasMap(aliasMap)
	}
	// D-127 P2：binance:perp stepSize 权威元数据（运行时 exchangeInfo + fallback）。
	stepMeta := dashboard.NewCexStepMeta(cfg.BinanceFuturesInfoURL, cfg.SymbolsTTL)
	dashSvc.SetStepMeta(stepMeta)
	// D-311 R4：okx:perp 腿最小张数 lotSz——instId→lotSz，复用 exec.OkxSwapSpec
	// instruments 公共端点+包级同源缓存（与执行器 okxContractVal 同一份缓存）。
	dashSvc.SetOkxLotSz(func(instID string) (float64, bool) {
		if !strings.HasSuffix(instID, "-SWAP") {
			instID += "-SWAP"
		}
		ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, lot, _, err := exec.OkxSwapSpec(ctx2, httpx.Client(), "https://www.okx.com", instID)
		if err != nil {
			log.Printf("[exec] okx lotSz %s: %v (fail-closed)", instID, err)
			return 0, false
		}
		return lot, true
	})
	safego.GoLoop("wire_dashboard:stepMeta", func() {
		stepMeta.MaybeRefresh(ctx) // 启动即拉一次
		ticker := time.NewTicker(cfg.SymbolsTTL)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stepMeta.MaybeRefresh(ctx)
			}
		}
	})
	dashSvc.SetFrontConfig(dashboard.FrontConfig{ // D-077 修复⑪B N7：前端常量后端同源
		NotifyMinNetBps:   5.0, // 修复⑭ S3：发布门槛退役，此值降级为前端显示过滤常量（语义自立，不再驱动发布）
		FreshnessTTLSec:   int(cfg.DetectorFreshnessTTL.Seconds()),
		EntrySpreadMinBps: cfg.SwapArbEntrySpreadMinBps,
		MinNetSwapBps:     cfg.AutoTradeMinNetSwapBps,
		RevalidateAbsBps:  cfg.RevalidateAbsBps,
		RevalidateRelPct:  cfg.RevalidateRelPct,
		VerifyWindowDays:  cfg.VerifyWindowDays, // D-086
		VerifyMinSamples:  cfg.VerifyMinSamples,
		VerifyMinRatio:    cfg.VerifyMinRatio,
	})
	dashSvc.SetTrxFloorPct(cfg.TrxEnergyRateFloorPct) // D-258 S3：注入 floor 给收益率雷达
	// D-091 前端分析员装配：无条件构造（env 全空也注入），DB-only 配置可激活分析员。
	// R3 返修③：原 gate 在 cfg.AIAPIKey != "" —— env key 空时纯 DB 配置无法激活（deps=nil →「分析员未配置」）。
	// 双空兜底已由 resolveAiConfig failed_precondition 覆盖（TestResolveAiConfigBothEmpty），勿重复造门。
	aiClient := analyst.NewClient(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
	aiClient.CallLog = analyst.LLMLogger(st, "dashboard_analyst") // D-246：fin_llm_call 落行
	aiTools := &analyst.Tools{ReadOnlyDSN: cfg.AIPGDSN, Store: st, KnRoot: analyst.KnRoot}
	aiTools.EnrichPosition = dashSvc.EnrichPositionForAnalyst() // R2 返修②：get_entity position 含派生字段
	aiTools.ProbeCaller = dashSvc.ProbeCallerForAnalyst()       // D-100：llm_probe 当问配置探针
	aiRate := analyst.NewRateLimiter(cfg.AnalystRatePerHour)
	dashSvc.SetAnalyst(aiClient, aiTools, aiRate, analyst.KnRoot, "docs/经验库/待归纳-分析员.md")

	// D-244：部门日报→经理摘要——接入面复用本函数 aiClient/aiTools/aiRate（~12 调用/天）；
	// 飞书扇出走直推（Notify/ChatID 缺=只 alerts 留痕）；fail-open 零业务阻塞。
	safego.GoLoop("main:deptreport", func() {
		deptreport.Run(ctx, deptreport.Deps{
			Store:          st,
			Tools:          aiTools,
			Rate:           aiRate,
			CapitalBaseUSD: cfg.CapitalBaseUSD, // §4.3 指标①硬线分母
			ResolveClient: func(ctx context.Context) (*analyst.Client, error) {
				c, err := kbreport.ResolveClient(ctx, st, cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
				if c != nil {
					c.CallLog = analyst.LLMLogger(st, "company_report") // D-246：fin_llm_call 落行
				}
				return c, err
			},
			Notify: feishu,
			ChatID: st.FeishuChatID,
			Beat:   func() { safego.Beat("main:deptreport") },
			// D-264 M4.4①③：日切钩——fin_nav_daily 幂等落行 + fin_strategy_budget
			// 配置器重算（确定性零 LLM；boot 首切+每班各一次）。
			OnDayCut: func(ctx context.Context, dayStart time.Time) error {
				if err := budget.DailyCut(ctx, st, time.Now(), dayStart, cfg.AutoTradeMaxNotional); err != nil {
					return err
				}
				// D-545② 快照保留窗（挂既有日切拍，零新定时器）：7d 前且有更新行的老行删，
				// 每键最新行永保（化石键=断采唯一证据）。
				if n, perr := st.PruneVenueAccountSnapshots(ctx, time.Now().Add(-7*24*time.Hour)); perr != nil {
					log.Printf("[daycut] snapshot prune: %v", perr)
				} else if n > 0 {
					log.Printf("[daycut] snapshot prune: %d 老行清除", n)
				}
				return nil
			},
		})
	})

	// D-048③：探针到期平仓取证报告钩——explorer.Deps 复用（store+ResolveClient+研报目录），
	// LLM 生成自开 goroutine（2min 超时），平仓路径零阻塞；报告推/告警走 alerts→feishu 扇出。
	probeRepDeps := explorer.Deps{
		Store: st,
		ResolveClient: func(ctx context.Context) (*analyst.Client, error) {
			c, err := kbreport.ResolveClient(ctx, st, cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIModel)
			if c != nil {
				c.CallLog = analyst.LLMLogger(st, "probe_report") // D-246：fin_llm_call 落行
			}
			return c, err
		},
	}
	dashSvc.SetProbeReporter(func(_ context.Context, posID int64) {
		safego.GoPlain("probe-report", func() {
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if _, _, err := explorer.GenerateProbeReport(rctx, probeRepDeps, posID); err != nil {
				log.Printf("[probe] report pos=%d: %v", posID, err)
			}
		})
	})
}
