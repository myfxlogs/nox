// Package dashboard 实现 DashboardService RPC handler（§8.2）。
package dashboard

import (
	"context"
	"strings"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/position"
	"arb/internal/store/pgstore"
)

// Service 实现 DashboardServiceHandler 接口。
type Service struct {
	store    *pgstore.Store
	qbus     *bus.QuoteBus
	pipeline *exec.Pipeline
	bm       *BrokerManager
	// reviewer 复盘触发（§13.6，D-034）。nil = 未接线（RPC 仍可用，仅触发不跑）。
	reviewer func()
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
	// priceTopic 实时现价广播（D-049）：PersonalMonitor 经 onPrice 发布 → StreamUpdates 扇出 case 7。nil = 未接线（测试直接构造 Service）。
	priceTopic *bus.Topic[position.PositionPrice]
	// oppTopic 新机会推送广播（D-050）：engine 新插入发布 → StreamUpdates 扇出 case 2。nil = 未接线（测试直接构造 Service）。
	oppTopic *bus.Topic[pgstore.Opportunity]
	// fundingNeedTopic 资金注入提醒广播（D-072）：engine 余额不足发布 → StreamUpdates 扇出 case 10。nil = 未接线（测试直接构造 Service）。
	fundingNeedTopic *bus.Topic[FundingNeedNotice]
	// orderTopic 持仓变更通知广播（D-107 P3）：OnOrderUpdate 事件 → StreamUpdates 扇出 case 11。nil = 未接线（测试直接构造 Service）。
	orderTopic *bus.Topic[bus.OrderEvent]
	// D-114：告警实时推送 topic（InsertAlert → store.alertHook → alertTopic.Publish → StreamUpdates case 3）。
	// nil = 未接线（测试直接构造 Service）→ 不订阅。
	alertTopic *bus.Topic[pgstore.Alert]
	// bpm Broker 实持仓 monitor（D-052）：ListBrokerPositions 拉 + StreamUpdates case 8 推同源。nil = 未接线（测试直接构造 Service）。
	bpm *BrokerPositionMonitor
	// notionalUSD net_usd 换算的单笔名义（config.OpportunityNotionalUSD，D-053）。0 = 未接线（测试），net_usd=0。
	notionalUSD float64
	// acctMon 账户资产 monitor（D-053）：ListAccounts 拉 + StreamUpdates case 9 推同源。nil = 未接线（测试直接构造 Service）。
	acctMon *AccountMonitor
	// tradeModes 单腿 trade_mode 查询（D-060 F5 执行预检，防裸腿）：canonical+venueKey → mode。
	// nil = 未接线（测试直接构造 Service，预检放行）。main 注入 factCache.TradeMode（不引入 exec→engine 依赖）。
	tradeModes func(canonical, venueKey string) (int64, bool)
	// legContractSize MT 腿合约规格查询（D-060 实施修正② 卖腿等名义反算）：venueKey+venueSymbol → CS。
	// nil = 未接线 → resolveLegs 报错（fail-safe）。main 接 broker_symbol_map.contract_size。
	legContractSize func(venueKey, venueSymbol string) (float64, bool)
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
	// D-140 S3：swap 组合列表注入（main 装配 autotrade 引擎的 discoverSwapCombos 适配）。
	// nil = 未接线（RPC 返回空列表）。避免 dashboard→autotrade 依赖环。
	swapComboLister func() []SwapComboEntry

	// D-111：收敛型实时 bps 成本扣除（commissionResolver 复用，nil = commissionBps=0）。
	commissionFor func(venueKey string) float64
	// D-111：收敛型实时 bps 滑点成本（cfg.SlippageBps，slippageCost = slippageBps * 2）。
	slippageBps float64
	// D-127 P2：binance:perp stepSize 权威元数据（运行时 exchangeInfo + fallback）。nil = 仅用 fallback。
	stepMeta *cexStepMeta
	// D-155 freeze guard：冻结腿预检窗口（config.FreezeGuard）。
	// 开仓前双腿各自校验「报价在窗口内发生过价格值变化」；≤0 = 禁用（全放行）。
	freezeGuard time.Duration

	// D-159 W1a R1：SWAPARB 精确组合门禁（手动入口同源）。
	// swapArbComboAllow 为 nil = 未接线（gate 层 fail-closed 兜底，手动入口不重复检查）。
	// 非 nil 时 ExecuteOpportunityInternal/ExecuteMeanRevertWithCap(SWAPARB) 在 pipeline.Execute 前检查。
	// ComboAllowSpec 定义在 exec 包（最底层无依赖，dashboard/autotrade 复用）。
	swapArbComboAllow []exec.ComboAllowSpec

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
}

// FrontConfig 前端展示阈值（D-077 修复⑪B N7，后端 config 单一真相）。
type FrontConfig struct {
	NotifyMinNetBps   float64
	FreshnessTTLSec   int
	EntrySpreadMinBps float64
	MinNetSwapBps     float64
	RevalidateAbsBps  float64
	RevalidateRelPct  float64
	// D-086 验证阶梯阈值（oppToProto 填 verified_level 用，与 engine 发布门同源）。
	VerifyWindowDays int
	VerifyMinSamples int
	VerifyMinRatio   float64
}

// New 创建 dashboard 服务。reviewer 为复盘触发闭包（nil 则平仓后不触发）；
// freshTTL 为报价陈旧阈值（个人持仓展示用，传 config.DetectorFreshnessTTL）；
// liqMMR 为个人持仓理论强平维持保证金率（传 config.LiqMMR）；
// fundingDropRatio 为资金费急降预警比例（传 config.FundingDropRatio）；
// swapArbEquityUseRatio 为 swap 套利名义额资金使用率系数（S6，传 config.SwapArbEquityUseRatio）；
// swapArbBareCap 为 swap 套利单腿裸露上限系数（D-097 R2，传 config.SwapArbBareCap）；
// swapArbVenueExposureCap 为单 venue SWAPARB 合计裸露上限系数（D-132 T1，传 config.SwapArbVenueExposureCap）；
// priceTopic 为实时现价广播 topic（D-049，传 main 创建的 priceTopic）；
// oppTopic 为新机会站内推送 topic（D-050，传 main 创建的 oppTopic）；
// bpm 为 Broker 实持仓 monitor（D-052，传 main 创建的 bpm）；
// notionalUSD 为 net_usd 换算的单笔名义（D-053，传 config.OpportunityNotionalUSD）；
// acctMon 为账户资产 monitor（D-053，传 main 创建的 acctMon）。
// orderTopic 为持仓变更通知广播（D-107 P3，传 main 创建的 orderTopic）。
// alertTopic 为告警实时推送广播（D-114，传 main 创建的 alertTopic；InsertAlert → store.alertHook → alertTopic.Publish → case 3）。
// commissionFor 为 per-venue 佣金解析器（D-111 收敛型实时 bps 成本扣除，nil = commissionBps=0）。
// slippageBps 为滑点成本（D-111，slippageCost = slippageBps * 2）。
func New(store *pgstore.Store, qbus *bus.QuoteBus, pipeline *exec.Pipeline, bm *BrokerManager, reviewer func(), freshTTL time.Duration, liqMMR float64, fundingDropRatio float64, swapArbEquityUseRatio float64, swapArbBareCap float64, swapArbVenueExposureCap float64, priceTopic *bus.Topic[position.PositionPrice], oppTopic *bus.Topic[pgstore.Opportunity], orderTopic *bus.Topic[bus.OrderEvent], alertTopic *bus.Topic[pgstore.Alert], bpm *BrokerPositionMonitor, notionalUSD float64, acctMon *AccountMonitor, commissionFor func(string) float64, slippageBps float64) *Service {
	return &Service{store: store, qbus: qbus, pipeline: pipeline, bm: bm, reviewer: reviewer, freshTTL: freshTTL, liqMMR: liqMMR, fundingDropRatio: fundingDropRatio, swapArbEquityUseRatio: swapArbEquityUseRatio, swapArbBareCap: swapArbBareCap, swapArbVenueExposureCap: swapArbVenueExposureCap, priceTopic: priceTopic, oppTopic: oppTopic, orderTopic: orderTopic, alertTopic: alertTopic, bpm: bpm, notionalUSD: notionalUSD, acctMon: acctMon, commissionFor: commissionFor, slippageBps: slippageBps}
}

// Store 返回底层 store（D-070 autotrade engine 用）。
func (s *Service) Store() *pgstore.Store { return s.store }

// AutoClosePosition 平仓（D-070 autotrade engine 自动平仓用，区别于 RPC ClosePosition）。
func (s *Service) AutoClosePosition(ctx context.Context, posID int64) error {
	return s.pipeline.ClosePosition(ctx, posID)
}

// ResumeClose D-189 P0-4：重触发 CLOSING 持仓平仓（reconcile 超时自动重试入口）。
// 只转发 pipeline.ResumeClose，不复制执行逻辑。
func (s *Service) ResumeClose(ctx context.Context, posID int64) error {
	return s.pipeline.ResumeClose(ctx, posID)
}

// ClaimClose D-189 R1：转发 store.ClaimClose（OPEN/LEGGED→CLOSING 原子领取）。
// reconcile auto_close step c 先 ClaimClose 再 ResumeClose，修复 position 仍 OPEN 时
// ResumeClose 拒绝非 CLOSING 导致永久卡死。
func (s *Service) ClaimClose(ctx context.Context, posID int64) error {
	return s.store.ClaimClose(ctx, posID)
}

// Settler 结算接口（D-087 §3.2，settlement.Settle 的注入面，dashboard 不依赖 settlement 包避免循环）。
type Settler interface {
	Settle(ctx context.Context, posID int64) error
}

// SetSettler 注入结算器（D-087 §3.2，手动平仓后调）。nil = 未接线（RPC 仍可用，仅不结算）。
func (s *Service) SetSettler(st Settler) { s.settler = st }

// SetTradeModes 注入单腿 trade_mode 查询（D-060 F5）。main 在 factCache 处装配；nil = 预检放行。
func (s *Service) SetTradeModes(f func(canonical, venueKey string) (int64, bool)) {
	s.tradeModes = f
}

// SetLegContractSize 注入 MT 腿合约规格查询（D-060 实施修正②）。nil = 未接线（resolveLegs 报错）。
func (s *Service) SetLegContractSize(f func(venueKey, venueSymbol string) (float64, bool)) {
	s.legContractSize = f
}

// LegContractSize D-165 S2：返回 legContractSize resolver（autotrade engine S2 notional 换算用）。
func (s *Service) LegContractSize() func(venueKey, venueSymbol string) (float64, bool) {
	return s.legContractSize
}

// SetMaxLegImbalancePct 注入卖腿名义不平衡上限（百分比；≤0 回退默认 2）。
func (s *Service) SetMaxLegImbalancePct(p float64) { s.maxLegImbalance = p }

// SetLegMinLots 注入 MT 腿最小手数查询（D-061）。nil/未知 = 放行。
func (s *Service) SetLegMinLots(f func(venueKey, venueSymbol string) (float64, bool)) {
	s.legMinLots = f
}

// SetLegVolumeStep 注入 MT 腿 volume step 查询（D-070 qty 精度取整）。nil/未知 = 用默认 0.01。
func (s *Service) SetLegVolumeStep(f func(venueKey, venueSymbol string) (float64, bool)) {
	s.legVolumeStep = f
}

// SetOppContractSize 注入机会卡 USD 派生用合约规格查询（D-062）。nil = 未接线 → USD 字段全 0（前端回退 bps）。
func (s *Service) SetOppContractSize(f func(canonical, venueKey string) (float64, bool)) {
	s.oppContractSize = f
}

// OppContractSize 透传机会卡 CS 查询（D-135 autotrade engine sizing 用）。
// nil 接线 → (0,false)；CEX 腿 CS=1 由 resolver 内部处理（与 D-062 同语义）。
func (s *Service) OppContractSize(canonical, venueKey string) (float64, bool) {
	if s.oppContractSize == nil {
		return 0, false
	}
	return s.oppContractSize(canonical, venueKey)
}

// SetFreezeGuard 注入冻结腿预检窗口（D-155）。≤0 = 禁用（全放行）。
func (s *Service) SetFreezeGuard(d time.Duration) { s.freezeGuard = d }

// SetStepMeta 注入 binance:perp stepSize 权威元数据（D-127 P2）。nil = 仅用硬编码 fallback。
func (s *Service) SetStepMeta(m *cexStepMeta) { s.stepMeta = m }

// SetSwapArbComboAllow 注入 SWAPARB 精确组合门禁 allowlist（D-159 W1a R1，手动入口同源）。
// nil/空 = 未接线（gate 层 fail-closed 兜底，手动入口不重复检查）。
func (s *Service) SetSwapArbComboAllow(allow []exec.ComboAllowSpec) { s.swapArbComboAllow = allow }

// SetEnvironment 注入执行环境固定值（D-159 W1a I6，手动入口同源）。
// 空 = 未配置（不检查，回退旧行为）。
func (s *Service) SetEnvironment(env string) { s.environment = env }

// SetLiveTrade 注入 LIVE 人工开关（D-159 W6a S2，手动入口同源）。
func (s *Service) SetLiveTrade(live bool) { s.liveTrade = live }

// SetAliasMap D-175：注入 venue→alias 缓存（main 启动时加载 ListAccountAliases）。
// nil/空 = 未接线（oppToProto alias 留空，前端回退 venueKey）。线程安全（RWMutex）。
func (s *Service) SetAliasMap(m map[string]string) {
	s.aliasMu.Lock()
	defer s.aliasMu.Unlock()
	s.aliasMap = m
}

// aliasForVenueKey D-175：从 venueKey="{venue}:{src}" 提取 venue 查 alias 缓存。
// venueKey 无冒号 → 整串当 venue 查；alias 空/未接线 → 返回空（前端回退 venueKey）。
func (s *Service) aliasForVenueKey(venueKey string) string {
	s.aliasMu.RLock()
	m := s.aliasMap
	s.aliasMu.RUnlock()
	if m == nil {
		return ""
	}
	venue := venueKey
	if idx := strings.Index(venueKey, ":"); idx > 0 {
		venue = venueKey[:idx]
	}
	return m[venue]
}
