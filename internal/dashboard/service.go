// Package dashboard 实现 DashboardService RPC handler（§8.2）。
package dashboard

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"arb/internal/bus"
	"arb/internal/capitalview"
	"arb/internal/collect/cmc"
	"arb/internal/config"
	"arb/internal/econ"
	"arb/internal/exec"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"
)

// Service 实现 DashboardServiceHandler 接口。
type Service struct {
	store    *pgstore.Store
	qbus     *bus.QuoteBus
	pipeline *exec.Pipeline
	// D-195-A：执行控制面检查器（避免 dashboard→autotrade 循环依赖，用接口注入）。
	// dashboard.New 内部自建 storeGateChecker（直接查 execution_controls）。
	gateChecker GateChecker
	bm          *BrokerManager
	// configKick D-469b S2/S5：config 类 mutation 成功路径回调（装配=bus.Topic.Publish；
	// nil=未接线不 kick）。消费方=三 cex manager+白名单/自选/告警热载器。
	configKick func()
	// reviewer 复盘触发（§13.6，D-034）。nil = 未接线（RPC 仍可用，仅触发不跑）。
	reviewer func()
	// D-478 S4：安全档位 cross 占比档（cfg.CrossNotionalEquityCap，SetCrossCap 注入）+
	// 恶化告警 transition 状态（内存态重启重置，per combo|venue ≥6h 去重对齐 D-472）。
	crossCap  float64
	tierState map[string]tierState
	tierMu    sync.Mutex
	// D-542：CEX 账户可用资金活账本（私有流主源+REST 校准回写，SetBalanceLedger 注入；
	// nil=未装配→腿池直走快照兜底）。liveMu=校准单飞；liveQuerierFor=测试注入点；
	// liveRestTTLDur 测试可缩短校准缓存。
	balLedger      *bus.BalanceLedger
	capView        *capitalview.View // D-549 S1：统一资金读口（capitalView() 惰性装配）
	liveMu         sync.Mutex
	liveQuerierFor func(ctx context.Context, venueKey string) (reconcile.AccountQuerier, string, bool)
	liveSimWarned  map[int64]bool
	liveRestTTLDur time.Duration
	acv            appConfigView // D-523：键视图快照+自杀重启注入（定义 service_appconfig.go）
	// D-545：密钥热读缓存（SetSecretResolver 注入；nil=未装配→Update 仅写库，热读点 fail-closed）。
	secRes *config.SecretResolver
	// D-546：资金全景三缺口实拉（CapitalMap 树用；60s 进程缓存=页面开即实拉、窗口内零
	// REST——A4 计数锚）。缺省 RPC 内自装配 binance real spot 行；测试注入计数 fake。
	capGaps       CapitalGapFetcher
	capGapsMu     sync.Mutex
	capGapsCache  *reconcile.CapitalGaps
	capGapsAt     time.Time
	capGapsTTLDur time.Duration
	// D-123：真北行年化分母（cfg.CapitalBaseUSD；≤0 → net_ann_30d 不可用 fail-closed）。
	capitalBaseUSD float64
	// D-546 R1：okx/gate 钱包树可用性（venueAvailability 60s 缓存+测试缝 capAvailFor）。
	capAvailMu    sync.Mutex
	capAvailCache map[string]*reconcile.VenueAvailability
	capAvailAt    map[string]time.Time
	capAvailFor   func(ctx context.Context, venue string) (venueAvailFetcher, bool)
	// 3d-P1#1c：无流子账户按需回读 Kick 防抖（10s/键；Kick 本身定向 REST 回读）。
	acctKickMu sync.Mutex
	acctKickAt map[string]time.Time

	// settler 结算触发（D-087 §3.2，手动平仓后调）。nil = 未接线（RPC 仍可用，仅不结算）。
	settler Settler
	// freshTTL 报价陈旧 TTL（复用 config.DetectorFreshnessTTL，G3）——个人持仓快照展示路径拒绝陈旧帧。
	freshTTL time.Duration
	// liqMMR 理论强平维持保证金率（config.ARB_LIQ_MMR，缺省 0.005）——个人持仓强平价理论值计算用。
	liqMMR float64
	// fundingDropRatio 资金费急降预警比例（config.ARB_FUNDING_DROP_RATIO，缺省 0.5）——§11 翻负/急降预警用。
	fundingDropRatio float64
	// swapArbEquityUseRatio swap 套利名义额资金使用率系数（config.ARB_SWAPARB_EQUITY_USE_RATIO，缺省 0.5）——S6 抗单余地。
	// notional = min(equity) × ratio；ratio=1 等价旧行为。0 = 未接线（测试，等价 ratio=1 旧行为）。
	swapArbEquityUseRatio float64
	// swapArbBareCap swap 套利单腿裸露上限系数（config.ARB_SWAPARB_BARE_CAP，缺省 1.0）——D-097 R2。
	// notional 若 > minEq×BareCap 则 cap 到 minEq×BareCap（只 cap 不 skip）。0 或负=不 cap（等价旧行为）。
	swapArbBareCap float64
	// swapArbVenueExposureCap 单 venue SWAPARB 合计裸露上限系数（config.ARB_SWAPARB_VENUE_EXPOSURE_CAP，缺省 2.0）——D-132 T1。
	// 新开仓时若任一腿 venue 现有 SWAPARB 活仓合计名义 + 新仓名义 > venue equity × cap → skip。0 = 不启用。
	swapArbVenueExposureCap float64
	// oppTopic 新机会推送广播（D-050）：engine 新插入发布 → StreamUpdates 扇出 case 2。nil = 未接线（测试直接构造 Service）。
	oppTopic *bus.Topic[pgstore.Opportunity]
	// fundingNeedTopic 资金注入提醒广播（D-072）：engine 余额不足发布 → StreamUpdates 扇出 case 10。nil = 未接线（测试直接构造 Service）。
	fundingNeedTopic *bus.Topic[FundingNeedNotice]
	// D-114：告警实时推送 topic（InsertAlert → store.alertHook → alertTopic.Publish → StreamUpdates case 3）。
	// nil = 未接线（测试直接构造 Service）→ 不订阅。
	alertTopic *bus.Topic[pgstore.Alert]
	// D-234：CMC 看盘实时帧 topic（帧钩子→1s 蓄压→computeCMCWatchUpdate→Publish→StreamUpdates case 13）。nil=未接线→不订阅。
	cmcWatchTopic *bus.Topic[*v1.CMCWatchUpdate]
	// D-508 S9：域数据刷新通知 topic（写源 Publish(topic) → StreamUpdates case 14）。nil=未接线→不订阅。
	refreshTopic *bus.Topic[string]
	// funds D-568：funds 通知尾沿合批态（NotifyFunds 三源汇一口）。
	funds fundsNotifier
	// D-007 S1：待入场闸态 diff 器（qbus 帧/allow_rules/看门狗三源标脏→元组 diff→pending_entries emit）。
	pgw     *pendingGateWatcher
	pgwOnce sync.Once
	// bpm Broker 实持仓 monitor（D-052）：ListBrokerPositions 拉 + StreamUpdates case 8 推同源。nil = 未接线（测试直接构造 Service）。
	bpm *BrokerPositionMonitor
	// notionalUSD net_usd 换算的单笔名义（config.OpportunityNotionalUSD，D-053）。0 = 未接线（测试），net_usd=0。
	notionalUSD float64
	// acctMon 账户资产 monitor（D-053）：ListAccounts 拉 + StreamUpdates case 9 推同源。nil = 未接线（测试直接构造 Service）。
	acctMon *AccountMonitor
	// streamOnlineFn D-571C-R2 F-1：venue 流在线态读源（SetStreamOnlineSource 注入→
	// assembleSourceHealth 编 venue_online 行；nil=缺该域）。数据源=bpMon.streamOnline。
	streamOnlineFn func() map[string]bool
	// posMon D-571 包C：positions(case4 复活)/personal(case18)/hedge(case17)/health(case5) 四域发射器。
	// nil = 未接线（测试直接构造 Service——RPC 组装路径不受影响，流订阅/回放零事件）。
	posMon *PositionMonitor
	// tradeModes 单腿 trade_mode 查询（D-060 F5 执行预检，防裸腿）：canonical+venueKey → mode。
	// nil = 未接线（测试直接构造 Service，预检放行）。main 注入 factCache.TradeMode（不引入 exec→engine 依赖）。
	tradeModes func(canonical, venueKey string) (int64, bool)
	// legContractSize MT 腿合约规格查询（D-060 实施修正② 卖腿等名义反算）：venueKey+venueSymbol → CS。
	// nil = 未接线 → resolveLegs 报错（fail-safe）。main 接 broker_symbol_map.contract_size。
	// 原生 CS 口径——配原生 PriceActual 消费方专用（偏差/结算/费用摊销）。
	legContractSize func(venueKey, venueSymbol string) (float64, bool)
	// legUnitQty D-594：MT 腿 canonical 单位数/手 = contract_size × qty_scale。
	// 配 canonical 报价链（legContractSizeOf/名义额/sizing）；敏感 canonical 未标定 → ok=false。
	legUnitQty func(venueKey, venueSymbol string) (float64, bool)
	// primarySymbol D-383 A2.4：执行解析 primary-only 闸（venueKey+canonical → 合格 broker_symbol）。
	// 多行组零 primary → 含 "symbol resolution ambiguous" 的错误（fail-closed 拒单）。
	primarySymbol func(venueKey, canonical string) (string, error)
	// cexContractSize CEX perp 腿合约面值查询（D-311 S3）：bare venue+venueSymbol → ct_val。
	// nil = 未接线 → legContractSizeOf 对 :perp 腿报错（fail-closed，不猜 1.0）。
	// main 接 fin_cex_universe.ct_val（gate=quanto_multiplier/okx=ctVal/binance=1.0）。
	cexContractSize func(venue, venueSymbol string) (float64, bool)
	// marginGuard D-405 S3：账户资金三档评估器（InitMarginGuard 装配；nil=未接线）。
	marginGuard *MarginGuard
	// maxLegImbalance 卖腿名义不平衡上限（百分比，默认 2，D-028/D-060 实施修正②）。
	maxLegImbalance float64
	// legMinLots MT 腿最小手数查询（D-061 量规格预检）：venueKey+venueSymbol → min lots。
	// nil/未知 = 放行（venue 服务端拒单兜底；position #3 实证：qty 0.1 < IC XBRUSD min 1.0 → LEGGED）。
	legMinLots func(venueKey, venueSymbol string) (float64, bool)
	// legVolumeStep MT 腿 volume step 查询（D-070 qty 精度取整）：venueKey+venueSymbol → step。
	// nil/未知 = 用默认 0.01（MT5 手数步长）。
	legVolumeStep func(venueKey, venueSymbol string) (float64, bool)
	// oppContractSize 机会卡 USD 派生用合约规格查询（D-062）：canonical+venueKey → CS。
	// nil = 未接线（测试直接构造 Service）→ USD 字段全 0，前端回退 bps。main 接 store.ContractSizeByCanonical。
	oppContractSize func(canonical, venueKey string) (float64, bool)
	// P5 修复⑧：告警 TTL 节流——上次 purge 时间（内存节流，按日一次；ListAlerts 顺带触发）。
	lastAlertPurge time.Time
	// frontCfg 前端展示阈值同源（D-077 修复⑪B N7 GetFrontConfig RPC）。
	frontCfg FrontConfig
	// D-090 自选报价条：watchlist 内存缓存（Set 全程持锁，case 1 RLock 命中检查）。
	// watchlistSet nil = 未初始化（首次 GetWatchlist 或 StreamUpdates 进 select 前查库填）。
	// 返修包① R1：值升级为 per-item venue 集合（空集合=全部源），key=canonical。
	watchlistMu       sync.RWMutex
	watchlistSet      map[string]map[string]struct{}
	watchlistInitOnce bool // true = 已查库初始化过（空表也置 true，避免重复查）
	// quoteFlushInterval StreamUpdates case 1 蓄压 flush 间隔（默认 1s，测试注 10ms）。
	quoteFlushInterval time.Duration
	// D-091 前端分析员依赖（main 经 SetAnalyst 注入；nil = 未配置，RPC 返回 failed_precondition）。
	analyst *analystDeps
	// D-048②：ApproveProbe 的 D-047 执行缝（nil=真实 s.OpenProbe；测试 fake 计数）。
	probeOpener func(ctx context.Context, r *v1.OpenProbeRequest) (*v1.OpenProbeResponse, error)
	// D-048③：探针到期平仓收尾钩子（nil=未接线；finalizeProbeClose 成功后同步调用，
	// 重活（LLM 报告）由注入方自开 goroutine——平仓路径不被 LLM 延迟阻塞）。
	probeReporter func(ctx context.Context, posID int64)
	// D-140 S3：swap 组合列表注入（main 装配 autotrade 引擎的 discoverSwapCombos 适配）。
	// nil = 未接线（RPC 返回空列表）。避免 dashboard→autotrade 依赖环。
	swapComboLister func() []SwapComboEntry

	// D-111：收敛型实时 bps 成本扣除（commissionResolver 复用，nil = commissionBps=0）。
	commissionFor func(venueKey string) float64
	// D-111：收敛型实时 bps 滑点成本（cfg.SlippageBps，slippageCost = slippageBps * 2）。
	slippageBps float64
	// D-391：回本列「本」口径 CEX 腿 fee_usd NULL 回退估算率（cfg.XAssetCexFeeBps，bps）。
	xassetCexFeeBps float64
	// D-127 P2 + D-466：binance:perp stepSize 权威元数据（运行时 exchangeInfo 唯一源，硬编码 fallback 已撤）。
	stepMeta *cexStepMeta
	// openVenueLegsFn D-009 S4 测试 seam：venue 实占行集注入（nil=store.OpenVenueSystemLegs）。
	openVenueLegsFn func(ctx context.Context, venueBare string) ([]pgstore.OpenVenueLegRow, error)
	// D-311 R4：okx:perp 腿最小张数 lotSz 查询（instId→lotSz，instruments 同源缓存）。
	// nil = 未接线 → okx:perp step=0 fail-closed。
	okxLotSz func(instID string) (float64, bool)
	// D-155 freeze guard：冻结腿预检窗口（config.FreezeGuard）。
	// 开仓前双腿各自校验「报价在窗口内发生过价格值变化」；≤0 = 禁用（全放行）。
	freezeGuard time.Duration

	// D-159 W1a R1：SWAPARB 精确组合门禁（手动入口同源）。
	// swapArbComboAllow 为 nil = 未接线（gate 层 fail-closed 兜底，手动入口不重复检查）。
	// 非 nil 时 ExecuteOpportunityInternal/ExecuteMeanRevertWithCap(SWAPARB) 在 pipeline.Execute 前检查。
	// ComboAllowSpec 定义在 exec 包（最底层无依赖，dashboard/autotrade 复用）。
	// D-515 S5：表化热读——atomic 快照（reloader 推入，与 xassetAllow 同模式）。
	swapArbComboAllow atomic.Pointer[[]exec.ComboAllowSpec]

	// D-311 S6：XASSET_CARRY 精确组合门禁（手动入口同源）——零信任显式白名单。
	// 与 SWAPARB 不同：无条件检查（空表=全拒），XASSET_CARRY 唯一放行闸。
	// D-403 R2：atomic 热刷新（approve 扇出写 vs 手动执行读竞态安全；Load nil=空集全拒）。
	xassetAllow atomic.Pointer[[]exec.XAssetComboAllowSpec]
	// D-403 R2：白名单加载环境（approve 扇出 rows 单查用；wire 传 cfg.Environment）。
	xassetEnv string

	// D-328 S5：carry 入口 funding regime 持久性闸参数（ExecuteOpportunityInternal 消费）。
	// 零值 = fail-closed（XASSET_CARRY 全拒）。
	carryRegime exec.CarryRegimeConfig

	// D-403 S3：验证毕业链依赖（nil-able setter 注入模式，New 已 20+ 参勿再加）。
	gradStore         graduationStore                                                                   // 测试替身 seam；nil=回落 s.store
	xassetReloader    XAssetReloader                                                                    // engine 热重载（nil=approve fail-closed 拒绝）
	allowVenuesSource func(ctx context.Context) (map[string]bool, error)                                // D-443 S4：venue 白名单表活读 getter（nil=空集保守全缺口）
	autoMapFn         func(ctx context.Context, venue string) (string, error)                           // AutoMap 摘要 seam（nil=真身 s.AutoMapBroker）
	rejectBreaker     RejectBreakerReader                                                               // D-560 S3：拒单熔断快照/恢复（nil=无透出）
	mtSummaryFn       func(ctx context.Context, venueKey string) (float64, float64, string, error)      // D-563 S4b：MT 实时资金缝（equity/freeMargin/currency；nil=bm 真身）
	venueLeverageFn   func(ctx context.Context, venueKey, symbol string, sellLeg bool) (float64, error) // D-565 S2：venue 杠杆实读缝（nil=venueLeverageExec 真身）
	// D-572 S1：腿池面板杠杆读穿缓存（键=vk|sym|side，TTL=5min——请求驱动防逐行渲染打爆 REST；无后台轮询）。
	levCacheMu sync.Mutex
	levCache   map[string]levCacheEntry
	// D-076：MT 腿池快照过期时的 acctMon 定向 kick 节流（per-vk ≥30s，防 1s 合批轮次连踢）。
	mtPoolKickMu sync.Mutex
	mtPoolKick   map[string]time.Time

	// perpLeverageTarget D-079：perp 腿目标杠杆（main 同源传 cfg.PerpLeverageTarget；
	// >0 且 venue 现值≠目标 → 保证金口径按策略值算=开仓前置 ensure 的真实状态）。
	perpLeverageTarget float64

	// D-159 W1a I6：environment/is_real 一致性检查（手动入口同源）。
	// environment 空 = 未配置（不检查，回退旧行为）。
	environment string
	// D-159 W6a S2：LIVE 人工开关（手动入口同源）。
	// LiveTrade=false 时 is_real=true 账户拒绝开仓。
	liveTrade bool

	// D-182 S4：swap 偏差拦截阈值（手动入口同源，与 autotrade.Gate.SwapDeviationGatePct 对齐）。
	// ≤0 = 禁用（默认）。main 从 cfg.SwapDeviationGatePct 注入。
	swapDeviationGatePct float64

	// D-175：venue→alias 缓存（main 启动时加载 ListAccountAliases 注入）。
	// oppToProto 热路径读此 map 填 alias_buy/alias_sell，避免每条机会查 DB。
	// nil = 未接线（alias 留空，前端回退 venueKey）。RWMutex 保护并发读/SetAliasMap 写。
	aliasMu  sync.RWMutex
	aliasMap map[string]string

	// D-318：结算口径参照缓存（settle=7d funding_history 均值 TTL 10min + pred=当期预测 TTL 60s）。
	// 模式同 aliasMu/aliasMap；定义与填充逻辑见 service_settle_ref.go。
	settleRef settleRefCache

	// D-193-A/R9：CMC ws 事件驱动健康检查（nil=未接线→wsConnected=false）。main 装配 ws.IsHealthy 注入。
	cmcWsHealth func() bool
	// D-236 变更9：CMC ws 内存 LiveState provider（nil=未接线→回退 DB）。main 装配 ws.LiveState 注入。
	// 替代 D-234 cmcAskProvider（单 seam 统一 position+summary 域）。
	cmcLiveProvider func() cmc.LiveState
	// D-234：CMC 看盘帧 1s 蓄压状态（NotifyCMCFrame→AfterFunc→flushCMCWatch 现算+Publish）。
	cmcFrameMu            sync.Mutex
	cmcFrameTimer         *time.Timer
	cmcWatchFlushInterval time.Duration
	// D-331：机会卡「已持仓」标识缓存（活跃仓 opportunity_id 集合，15s TTL；
	// ListOpportunities/StreamUpdates 热路径防 N+1 直查。查询失败保上次缓存）。
	activeOppMu  sync.Mutex
	activeOppIDs map[string]bool
	activeOppAt  time.Time
	// D-340 S3：XASSET 当前净 carry 引擎包装注入（与退出判定同源）+15s TTL 缓存
	//（OPEN 息差型年化对照列）。nil=未注入（live 缺省，诚实空态）。
	xassetCarryLive func(ctx context.Context, posID int64) (float64, bool)
	carryMu         sync.Mutex
	carryLive       map[int64]float64
	carryLiveAt     map[int64]time.Time
	// D-014 ④：econ 同源注入（autotrade 装配面）。nil=未接线 → 回本列缺省（诚实空态）。
	econPosFn   func(ctx context.Context, posID int64) (econ.Snapshot, error)
	econEntryFn func(ctx context.Context, venueBuy, venueSell, canonBuy, canonSell string, legNotionalUSD float64) (float64, bool)
	// D-249 S3：scheduler kick 注入（on-open 触发采集补轮；nil=未接线→handler no-op）。
	kickFn func(string)
	// D-252：CMC 净值阈值告警状态机（nil=未接线→flushCMCWatch 不检查）。
	cmcEquityAlert *cmcEquityAlertMachine
	// D-258 S3：TRX 能量租金率 floor 告警阈值（ARB_TRX_ENERGY_RATE_FLOOR_PCT，默认 8.0；0=禁用）。
	trxFloorPct float64
	// D-347：可注入时钟（repo 雷达「当日」OHLC 交易日计算；nil = time.Now 生产墙钟）。
	nowFn func() time.Time
	// D-348：财务总览聚合 15s TTL 缓存（整体响应缓存，无失效钩子；同 D-331 语义）。
	financeMu       sync.Mutex
	financeCached   *v1.GetFinanceOverviewResponse
	financeCachedAt time.Time
	// D-352：纸面轨前端卡 15s TTL 缓存（financeOverview 同款）+ 单测 fake 注入源。
	paperBoardMu       sync.Mutex
	paperBoardCached   *v1.ListPaperSpotCarryResponse
	paperBoardCachedAt time.Time
	paperBoardSrc      paperBoardSource
}
