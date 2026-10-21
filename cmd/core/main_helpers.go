package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/bus"
	"nox/internal/data/collect/exchange"
	"nox/internal/data/fact"
	"nox/internal/platform/config"
	"nox/internal/platform/cryptoutil"
	"nox/internal/platform/safego"
	"nox/internal/research/detector"
	"nox/internal/research/evaluator"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
	"nox/internal/trading/autotrade"
	"nox/internal/trading/engine"
	"nox/internal/trading/exec"
)

// initAssetEquiv 建 asset_equiv 运行时视图并首载（D-300 裁 5 单实例：engine 快照扩展
// + detector 共享）。首载失败=空视图 fail-open（XAssetCarryDetector 零产出），运行期
// MaybeRefresh 由引擎报价事件驱动续试。
func initAssetEquiv(ctx context.Context, st *pgstore.Store) *pgstore.AssetEquivProvider {
	p := pgstore.NewAssetEquivProvider(st, 60*time.Second)
	if err := p.Refresh(ctx); err != nil {
		log.Printf("[main] asset_equiv load failed (fail-open): %v", err)
	}
	return p
}

// assetEquivSource 适配 pgstore.AssetEquivProvider → detector.AssetEquivSource
// （D-300 裁 5：engine 与 detector 共享同一 provider 实例；detector 不 import store 层）。
type assetEquivSource struct{ p *pgstore.AssetEquivProvider }

func (a assetEquivSource) ByClass() map[string][]detector.AssetEquivEntry {
	m := a.p.ByClass()
	out := make(map[string][]detector.AssetEquivEntry, len(m))
	for k, rows := range m {
		for _, r := range rows {
			out[k] = append(out[k], detector.AssetEquivEntry{
				AssetClass: r.AssetClass, Canonical: r.Canonical, VenueType: r.VenueType, Venue: r.Venue,
			})
		}
	}
	return out
}

// buildDetectors 构造检测器集合（§5）——从 main.go 拆出（文件行数治理）。
// equiv 为 asset_equiv 运行时视图（D-300；nil 时 XAssetCarryDetector 零产出）。
// st 供 D-403 S4 cfd universe-hint 配对源（nil 时 XAssetCarryDetector 无 hint 路径，向后兼容）。
// xassetAllow D-017 发现域收口：detector 白名单前置滤（延迟绑定，见 xassetAllowRef）。
func buildDetectors(cfg *config.Config, equiv *pgstore.AssetEquivProvider, st *pgstore.Store, xassetAllow func(canonical, canonicalSell, venueBuy, venueSell string) bool) []detector.Detector {
	return []detector.Detector{
		// D-512 T6：CrossVenueDetector 退役摘除（14d 前置闸实测 0 行；枚举/历史行/展示侧保留）。

		&detector.FundingHedgeDetector{
			MinNetAnnualized: cfg.FundingMinNetAnnualized,
			BaselinePct:      cfg.FundingBaselinePct, // D-084 同所基准费率扣除
			FreshnessTTL:     cfg.DetectorFreshnessTTL,
			Allow:            xassetAllow,
			AbsentFn:         fundingAbsentFn(st), // STATE 行50：venue 无 funding 面在册不产候选
		},
		// D-187：CarryDetector 已删除——CARRY 候选由 autotrade.discoverSwapCombos 统一产出。
		&detector.PerpFundingDetector{
			MinNetAnnualized: cfg.PerpFundingMinNetAnnualized,
			FreshnessTTL:     cfg.DetectorFreshnessTTL,
		},
		// D-300：跨标的同质资产套利（同 asset_class 跨 canonical 配对）。
		&detector.XAssetCarryDetector{
			EntryMinBps:     cfg.XassetEntryMinBps,
			MinNetAnnualPct: cfg.XassetMinNetAnnualPct,
			TrackingMaxBps:  cfg.XassetTrackingMaxBps,
			FreshnessTTL:    cfg.DetectorFreshnessTTL,
			AssetEquiv:      assetEquivSource{equiv},
			CfdHints:        newCfdHintSource(st),           // D-403 S4：universe-hint 配对源
			DeliveryHints:   newArmedDeliveryHintSource(st), // D-649续③：armed 交割配对源
			Allow:           xassetAllow,
		},
	}
}

// xassetAllowRef D-017 R1：detector Allow → autotrade Engine 热重载 holder 延迟绑定。
// 装配序上 detectors（main §3）先于 atEngine（runServices §8b）——经原子 holder 接通，
// 引擎装配后 Bind；未接通（boot 窗口）=零候选 fail-closed。无第二加载器无 TTL 缓存：
// 读口=Engine.XAssetAllowMatch 直读 xassetAllow 热重载 holder（configKick→Reload 即时
// 生效），白名单增删在 Reload 后下一帧发现立即反映。
type xassetAllowRef struct {
	eng atomic.Pointer[autotrade.Engine]
}

func (r *xassetAllowRef) match(canonical, canonicalSell, venueBuy, venueSell string) bool {
	e := r.eng.Load()
	return e != nil && e.XAssetAllowMatch(canonical, canonicalSell, venueBuy, venueSell)
}

// Bind 装配收口：atEngine 构造后接通（runServices 内调用一次）。
func (r *xassetAllowRef) Bind(e *autotrade.Engine) { r.eng.Store(e) }

// buildEvalConfig 构造评估器配置（§12）——从 main.go 拆出（文件行数治理）。
func buildEvalConfig(cfg *config.Config) evaluator.Config {
	return evaluator.Config{
		SlippageBps:             cfg.SlippageBps,
		MarginRateAnnual:        cfg.MarginRateAnnual,
		DetectorFreshnessTTL:    cfg.DetectorFreshnessTTL,
		OpportunityTTL:          cfg.OpportunityTTL,
		GrabMinNetAnnualized:    cfg.GrabMinNetAnnualized,
		FundingMaxBreakevenDays: cfg.FundingMaxBreakevenDays, // D-133 T2 回本周期上门
	}
}

// fundingHistoryResolver D-172 S1：返回某 venue×symbol 的最近 funding_history 时间戳，
// 无历史返回零时间（首次回填 30 天）。用于历史采集器增量补采避免重复。
func fundingHistoryResolver(st *pgstore.Store) exchange.HistoryStartResolver {
	return func(ctx context.Context, venue, symbol string) (time.Time, error) {
		facts, err := st.LatestFacts(ctx, fact.KindFundingHistory, venue, symbol)
		if err != nil {
			return time.Time{}, err
		}
		if len(facts) == 0 {
			return time.Time{}, nil
		}
		return facts[0].Ts, nil
	}
}

// refreshDisabledStrategies 查询 strategy_stats.disabled=true 集合并注入 engine（§1 自动淘汰闭环）。
// 初始加载 + LISTEN 事件触发全量刷新（淘汰判定在运行时触发，事件驱动替代轮询）。
func refreshDisabledStrategies(ctx context.Context, st *pgstore.Store, eng *engine.Engine) {
	disabled, err := st.ListDisabledStrategies(ctx)
	if err != nil {
		log.Printf("[main] ListDisabledStrategies (fail-open): %v", err)
		return
	}
	eng.SetDisabledStrategies(disabled)
}

// startDisabledStrategiesListener 初始加载 + LISTEN strategy_stats 频道驱动刷新（D-004 P1#3）。
// 写者含进程外（cmd/resettle、psql 手改）→ 用 DB 触发器 NOTIFY（迁移 0163）而非进程内
// topic；（重）LISTEN 成功后兜底全量刷新，兜住断线间隙漏掉的通知。
func startDisabledStrategiesListener(ctx context.Context, st *pgstore.Store, eng *engine.Engine) {
	refreshDisabledStrategies(ctx, st, eng) // 初始加载
	safego.GoLoop("main_helpers:disabledStrategiesListener", func() {
		st.Listen(ctx, "strategy_stats", func() { refreshDisabledStrategies(ctx, st, eng) })
	})
}

// startCollectorsAndScanners D-474a：main() ≤300 行门禁——income/IO 采集器与扫描器
// 批量装配自 main() 提取（纯搬家；kick 通道 buffered(1) 非阻塞合并，定时路径保留为断流兜底）。
func startCollectorsAndScanners(ctx context.Context, st *pgstore.Store, cfg *config.Config,
	cexCfg exchange.Config, cexSymbols []string,
	incomeKickCh, okxIncomeKickCh, ioKickCh chan struct{}, refreshTopic *bus.Topic[string],
	probeTopic *bus.Topic[struct{}], brokerMgr *dashboard.BrokerManager, cp *gate.ControlPlane) {
	startIncomeCollector(ctx, st, cexCfg, cexSymbols, incomeKickCh)         // D-334ab S3：income 实数轨结算对齐采集
	startOKXIncomeCollector(ctx, st, cexCfg, okxIncomeKickCh)               // D-385 S3：okx bills→income_events（15min 轮询）
	startCexIOCollector(ctx, st, ioKickCh, brokerMgr)                       // D-365 S3：CEX 进出账采集 15min 水位增量
	WarnXAssetEnvDeprecated(cfg)                                            // D-338 S4：env 白名单弃用告警
	startCarryUniverseScanner(ctx, st, cfg.TradeModeTTL, cfg, refreshTopic) // D-336 S1：carry 宇宙扫描器（发现层 4h）
	startKbReporter(ctx, st, cfg)                                           // D-442 S3：KB 元分析周报器（周 ticker 落档）
	startRetroReporter(ctx, st, cfg)                                        // D-457 S3：系统复盘日结+周复盘双定时器
	startOppMatch(ctx, st, cfg, cp)                                         // D-187：模板×账本日频匹配→提案/缺口
	startExplorer(ctx, st, cfg, probeTopic)                                 // D-513 S5：探矿员壳（env 闸缺省关=零 goroutine）
	startSleepmon(ctx, st, cfg)                                             // D-534 B4：睡钱触发层（BJ 日检 7d 破地板→alert+inbox）
	startDecisionOutcomeRunner(ctx, st)                                     // D-561：裁定归因簿（物化+30d 归因+连败告警）
}

// seedConfigFromEnv D-515 S4：app_config/app_secrets **空表时**把 env 值一次性入库
// （ON CONFLICT DO NOTHING，幂等）；非空则纯读。secrets 需 MASTER_KEY——缺则跳过该族播种
// （warn 不致命：空表+无 KEY 是合法形态，fatal 闸在 LoadAppSecrets 读面）。
// seedConfigFromEnv D-145 改语义：seed-once=boot 时 env→PG「仅缺键种入」（InsertIfAbsent
// 幂等，已有行永不覆写——此后 env 改名失效 PG 唯一真相；整表空门摘=新键后续补登也种）。
func seedConfigFromEnv(ctx context.Context, st *pgstore.Store) {
	for _, k := range config.AppConfigSeedKeys() {
		v, ok := os.LookupEnv(k)
		if !ok || v == "" {
			continue
		}
		if err := st.InsertAppConfigIfAbsent(ctx, k, v); err != nil {
			log.Fatalf("[config] seed %s: %v", k, err)
		}
	}
	key, ok := cryptoutil.MasterKey()
	if !ok {
		log.Printf("[config] app_secrets seed skipped (NOX_MASTER_KEY missing)")
		return
	}
	for _, k := range config.AppSecretSeedKeys() {
		v, ok := os.LookupEnv(k)
		if !ok || v == "" {
			continue
		}
		enc, err := cryptoutil.Seal(key, v)
		if err != nil {
			log.Fatalf("[config] seal secret %s: %v", k, err)
		}
		if err := st.InsertAppSecretIfAbsent(ctx, k, enc); err != nil {
			log.Fatalf("[config] seed secret %s: %v", k, err)
		}
	}
}

// applyAppSecretsOrFatal D-515 S3：app_secrets 解密注入 env（须在 config.Load 前；
// MASTER_KEY 缺+表非空 → fatal，fail-closed）。值永不进日志。
// D-545：返回解密集复用为 SecretResolver boot 种子（LoadAppSecrets 只跑一遍）。
func applyAppSecretsOrFatal(ctx context.Context, st *pgstore.Store) map[string]string {
	secrets, err := st.LoadAppSecrets(ctx)
	if err != nil {
		log.Fatalf("[config] app_secrets load: %v (startup fail-closed)", err)
	}
	config.ApplyAppSecretsOverlay(secrets)
	return secrets
}

// newSecretResolver D-545 S5：密钥热读缓存——种子=DB 解密集∪env 回落（DB>env 同 overlay
// 语义）；标量 NOX_MACRO_ENABLED 借同一轨（热闸）。UpdateAppSecret/UpdateAppConfig 写后同步 Set 即效。
func newSecretResolver(secrets map[string]string, cfg *config.Config) *config.SecretResolver {
	res := config.NewSecretResolver(secrets)
	for _, k := range config.AppSecretSeedKeys() {
		if _, ok := secrets[k]; !ok {
			res.Set(k, os.Getenv(k))
		}
	}
	res.Set("NOX_MACRO_ENABLED", strconv.FormatBool(cfg.MacroEnabled))
	return res
}

// sealExchangeKeysOrFatal D-515 S2：exchange_configs 密钥 boot 自封（明文→enc 清空留痕；
// MASTER_KEY 缺失且存在需封行 → fatal，fail-closed）。
func sealExchangeKeysOrFatal(ctx context.Context, st *pgstore.Store) {
	n, err := st.SealExchangeKeys(ctx)
	if err != nil {
		log.Fatalf("[crypto] seal exchange keys: %v (startup fail-closed)", err)
	}
	if n > 0 {
		log.Printf("[crypto] sealed %d exchange_configs rows", n)
	}
}

// newExecPipeline 执行管线装配（原 main() 段 7 提取，保 main.go ≤300 行）。
// 各阈值 setter 语义见行内 D#。
func newExecPipeline(execReg *exec.Registry, st *pgstore.Store, qbus *bus.QuoteBus, cfg *config.Config, pv *pipelineVerifier) *exec.Pipeline {
	exec.SetLegTickWindow(cfg.LegTickWindow) // D-145：残腿可平性窗 cfg 注入（原 env 直读）
	pipeline := exec.NewPipeline(execReg, st, qbus, exec.RiskLimits{
		MaxNotionalUSD:     cfg.AutoTradeMaxNotional,
		MaxOpenPositions:   cfg.AutoTradeMaxPositions,
		MaxLegImbalancePct: cfg.MaxLegImbalancePct,
	}, cfg.Environment)
	pipeline.SetRevalidateThresholds(cfg.RevalidateAbsBps, cfg.RevalidateRelPct)   // D-077 修复⑪B N4
	pipeline.SetPositionVerifier(pv)                                               // D-108 P1 R1：verifier 生产接线
	pipeline.SetMTDisasterSlPct(cfg.MTDisasterSlPct)                               // D-146 S1：MT 灾难 SL
	pipeline.SetMakerPolicy(cfg.MakerFirstLegTimeout, cfg.MakerMaxAttemptsPerHour) // D-018 阶段a：两键缺=maker 停用（fail-closed 既有市价路径）
	pipeline.SetPerpLeverageTarget(cfg.PerpLeverageTarget)                         // D-079：perp 腿开仓前置 set-leverage（≤0=停用保旧行为）
	pipeline.SetMTFirst(cfg.XAssetMTFirst)                                         // D-084：XASSET/FH 双腿 MT 先行（缺省 true；false=并发旧路径）
	return pipeline
}

// mustMoneyStore D-633 拆票：钱径保底池（exec/settle/position 写读独占——通用池读面爆量不饿钱径）。
func mustMoneyStore(ctx context.Context, dsn string) *pgstore.Store {
	st, err := pgstore.NewMoneyStore(ctx, dsn)
	if err != nil {
		log.Fatalf("db money pool: %v", err)
	}
	return st
}
