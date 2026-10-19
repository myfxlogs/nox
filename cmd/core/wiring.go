package main

import (
	"context"
	"log"
	"strings"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/collect"
	"nox/internal/data/collect/deribit"
	"nox/internal/data/collect/exchange"
	"nox/internal/data/keydomain"
	"nox/internal/data/normalize"
	"nox/internal/ledger/reconcile"
	"nox/internal/platform/alertcool"
	"nox/internal/platform/config"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// commissionResolver 构建 per-venue 佣金解析器（D-036）：
//   - CFD（:mt4/:mt5）：broker_accounts.commission_bps 账号真实值优先，缺省回退 cfg.CommissionCFDBps；
//   - CEX：逐交易所 taker（cfg.CommissionTaker）优先，缺省回退 cfg.CommissionCEXTakerBps。
func commissionResolver(cfg *config.Config, bm *dashboard.BrokerManager) func(venueKey string) float64 {
	return func(venueKey string) float64 {
		venue := keydomain.BareOf(venueKey)
		if keydomain.IsMTKey(venueKey) {
			if bps, ok := bm.CommissionBps(venue); ok {
				return bps // 账号真实值优先（D-533 calib 覆盖链已随 D-120 退役）
			}
			return cfg.CommissionCFDBps
		}
		if bps, ok := cfg.CommissionTaker[venue]; ok {
			return bps
		}
		return cfg.CommissionCEXTakerBps
	}
}

func maskDSN(dsn string) string {
	at := strings.IndexByte(dsn, '@')
	if at < 0 {
		return dsn
	}
	si := strings.Index(dsn, "://")
	if si < 0 {
		return dsn
	}
	return dsn[:si+3] + "***" + dsn[at:]
}

// sampleCanonicals 从 broker_symbol_map 派生去重 canonical（TickSampler 采样集，D-060 实施修正①）。
// DB 读失败/空集回退旧三符号（EURUSD/GBPUSD/XAUUSD）——采样缺集可修，不阻塞启动。
func sampleCanonicals(ctx context.Context, st *pgstore.Store) []string {
	accounts, err := st.ListBrokerAccounts(ctx)
	if err != nil {
		log.Printf("[sampler] list accounts: %v（回退 EURUSD/GBPUSD/XAUUSD）", err)
		return []string{"EURUSD", "GBPUSD", "XAUUSD"}
	}
	seen := map[string]bool{}
	var out []string
	for _, a := range accounts {
		maps, err := st.ListSymbolMappings(ctx, a.Venue)
		if err != nil {
			log.Printf("[sampler] list mappings %s: %v", a.Venue, err)
			continue
		}
		for _, m := range maps {
			if !seen[m.Canonical] {
				seen[m.Canonical] = true
				out = append(out, m.Canonical)
			}
		}
	}
	if len(out) == 0 {
		return []string{"EURUSD", "GBPUSD", "XAUUSD"}
	}
	return out
}

// 合约规格解析器组（legContractSizeResolver/cexContractSizeResolver/pipelineCSResolver/
// execCSResolver/oppContractSizeResolver）已拆至 wiring_cs.go（D-311 R2 行数治理）。

// === 修复⑫：reconcile → exec 三注入适配器（⑪A M1 PnL 校准 + CS 复用） ===
// 适配器放 main 层（零新包依赖）；reconcile 门面 → exec 注入接口字段直转。
// 编译期断言防接口漂移——SetXxx 签名变即编译爆。
// D-108 P1 R1：PositionVerifier 适配器移至 pipeline_verifier.go（per-call 10s 超时 + set 刷新）。

type execPnLCalibrator struct{ r *reconcile.Reconciler }

func (c execPnLCalibrator) QueryHistoryPnL(ctx context.Context, venueKey, ticket string, fromDays int) (*exec.HistoryPnL, error) {
	h, err := c.r.HistoryByVenueKey(ctx, venueKey, ticket, fromDays)
	if err != nil || h == nil {
		return nil, err // 未注册/未命中 → nil（同步公式兜底）
	}
	return &exec.HistoryPnL{Profit: h.Profit, Swap: h.Swap, Commission: h.Commission, ClosePrice: h.ClosePrice}, nil
}

// HasHistoryQuerier D-422：可校准性走既有 reconcile 路由（含 DB 账户动态注册），
// exec 侧 calibratePnL 按此区分"应校准腿"与"CEX 公式价腿"。
func (c execPnLCalibrator) HasHistoryQuerier(venueKey string) bool {
	return c.r.HasHistoryQuerier(venueKey)
}

var _ exec.PnLCalibrator = execPnLCalibrator{}

// runRuleBackfill D-140 S1：启动后规则库自动补录。
// 等待 broker 连接 → 逐 venue 拉取服务器符号表 → exact 未命中的跑规则库 →
// 落表 source='rule'（canonical 归一即生效，CS 留 NULL=CS 门继续拦）。
// 幂等：已有映射跳过。best-effort：失败不阻塞启动。
// D-142 ②：单 venue 逻辑提取为 backfillVenueRules，AddBroker 异步触发复用同函数。
func runRuleBackfill(ctx context.Context, st *pgstore.Store, bm *dashboard.BrokerManager) {
	eng := normalize.NewRuleEngine()
	// 等待 broker 连接（StartAll 异步，延迟拉符号表）
	time.Sleep(15 * time.Second)
	accounts, err := st.ListBrokerAccounts(ctx)
	if err != nil {
		log.Printf("[normalize] backfill: list accounts: %v", err)
		return
	}
	total := 0
	for _, a := range accounts {
		n := backfillVenueRules(ctx, st, bm, a.Venue, eng)
		total += n
		if n > 0 {
			log.Printf("[normalize] backfill %s: +%d rule-mapped (CS=NULL, CS gate still active)", a.Venue, n)
		}
	}
	log.Printf("[normalize] backfill done: %d new rule-mapped rows across %d venues", total, len(accounts))
}

// backfillVenueRules D-142 ②：单 venue 规则库补录（boot runRuleBackfill + AddBroker 异步触发共用）。
// 拉取 venue 服务器符号表 → BackfillRuleMappings（幂等，已有映射跳过）。返回新增行数。
func backfillVenueRules(ctx context.Context, st *pgstore.Store, bm *dashboard.BrokerManager, venue string, eng *normalize.RuleEngine) int {
	groups, err := bm.BrokerSymbols(ctx, venue)
	if err != nil {
		log.Printf("[normalize] backfill %s: broker symbols: %v", venue, err)
		return 0
	}
	symbols := make([]string, 0, len(groups))
	for _, g := range groups {
		symbols = append(symbols, g.Symbol)
	}
	n, err := st.BackfillRuleMappings(ctx, venue, symbols, eng)
	if err != nil {
		log.Printf("[normalize] backfill %s: %v", venue, err)
		return 0
	}
	if n > 0 {
		log.Printf("[normalize] backfill %s: +%d rule-mapped (AddBroker async)", venue, n)
	}
	return n
}

// buildDeribitCollector D-137/D-144 ④：Deribit 期权平价偏离采集监控装配（从 main.go 拆出控行数）。
// env 开关 NOX_DERIBIT_OPT_ENABLED 默认开；告警阈值 NOX_OPT_PARITY_ALERT_BPS 默认 15。
// 机会引擎零改动——纯监控旁路，不进检测/评估/执行路径。返回空切片=禁用。
// alertCooler（D-301c S2）：连败告警（计数递增+明细）是同指纹冷却的头号噪音源。
func buildDeribitCollector(st *pgstore.Store, alertCooler *alertcool.Cooler, cfg *config.Config) []collect.Named {
	if !cfg.DeribitOptEnabled {
		log.Println("config: deribit opt parity monitor disabled (NOX_DERIBIT_OPT_ENABLED=false)")
		return nil
	}
	deribitAlertFn := alertCooler.Wrap1(func(level, msg string) {
		if err := st.InsertAlert(context.Background(), nil, level, msg); err != nil {
			log.Printf("[deribit] alert insert: %v", err)
		}
	})
	dc := deribit.NewCollector(deribitAlertFn, st)
	dc.AlertBps = cfg.OptParityAlertBps
	if dc.AlertBps <= 0 {
		dc.AlertBps = deribit.DefaultAlertBps
	}
	log.Printf("config: deribit opt parity monitor enabled (alert_bps=%.1f)", dc.AlertBps)
	return []collect.Named{{
		Name: "deribit_opt_parity", Interval: deribit.DefaultInterval, Collector: dc,
	}}
}

// newAlertCooler D-301c S2：告警同指纹冷却单实例唯一创建点——main 域经此取用，四处 AlertFn
// 注入点共享透传（禁跨包全局态）；main.go 不 import alertcool 保 ≤300 行（R1）。
func newAlertCooler() *alertcool.Cooler { return alertcool.New(alertcool.DefaultWindow) }

// buildCexFundingCollectors 组装 CEX funding 轮询 + 结算历史采集器（D-178 提取自主函数保 ≤300 行）。
// D-301a S2：boot 时拉一次 Binance fundingInfo 缓存 per-symbol 结算周期（PAXG/XAUT 4h→6/天），
// 实时路径与 history 路径共用该表；失败 best-effort 回退 3（8h，D-079 禁静默）。
func buildCexFundingCollectors(st *pgstore.Store, cexCfg exchange.Config, alertCooler *alertcool.Cooler, depthExtraBases func(venue string) []string, intervals *exchange.FundingIntervalCache, cfg *config.Config) []collect.Named {
	hr := fundingHistoryResolver(st)
	binanceFundHist := exchange.NewBinanceFundingHistory(cexCfg, hr)
	binanceFundHist.SetFundingIntervals(intervals)

	// D-518 S3：三所 REST funding 5min 轮询退役——WS 原生流（binance_ws_markprice/okx_ws_v5/gate_ws_futures）
	// 是唯一 funding fact 源；`*_funding_history` 24h 回填器与 gate_contract_meta 元数据轮保留（用途不同）。
	cs := []collect.Named{
		// D-509 S3：Gate 合约元数据日轮（funding_interval 唯一职责；rate/next_apply 走 WS 帧）。
		{Name: "gate_contract_meta", Interval: 24 * time.Hour, Collector: exchange.NewGateContractMeta(cexCfg)},
	}
	// D-420 S2：盘口顶档深度采样（白名单符号 5min 节奏，容量与滑点研究数据前提）；
	// D-280 方案 A：extraBases=分所 funding 见证符号基名（候选腿真实集合零空打）。
	cs = append(cs, buildDepthCollector(cexCfg, st, depthExtraBases))
	// D-508 S3：facts kind 级 TTL + cex_earn_apr 30d 日轮 retention。
	// D-152 fixup：retention 是批量维护任务非采集源——首轮回填六表存量（~150 万行）
	// + 稳态 facts 日增量，35s 缺省预算必超时（生产实证 boot 轮六连 deadline exceeded）。
	cs = append(cs, collect.Named{Name: "retention", Interval: 24 * time.Hour, PollTimeout: 30 * time.Minute, Collector: &retentionCollector{st: st}})
	// §6.1③：发现漏斗堵点自盯日轮（假设/提案/毕业三段积压+停滞→warn 告警）。
	cs = append(cs, collect.Named{Name: "pipeline_watch", Interval: 24 * time.Hour, Collector: &pipelineWatchCollector{st: st}})

	// D-137/D-144 ④：Deribit 期权平价偏离采集监控装配。
	cs = append(cs, buildDeribitCollector(st, alertCooler, cfg)...)
	// D-172 S1：funding 结算历史采集（24h 增量补采，首次回填 30 天；结算费率非轮询预测值）。
	cs = append(cs,
		collect.Named{Name: "binance_funding_history", Interval: 24 * time.Hour, Collector: binanceFundHist},
		collect.Named{Name: "gate_funding_history", Interval: 24 * time.Hour, Collector: exchange.NewGateFundingHistory(cexCfg, hr)},
		collect.Named{Name: "okx_funding_history", Interval: 24 * time.Hour, PollTimeout: 10 * time.Minute, Collector: exchange.NewOKXFundingHistory(cexCfg, hr)}, // D-398 S2：百级符号串行探测需放宽预算（24h 轮无时效压力）
	)
	return cs
}
