// Package dashboard 实现 DashboardService RPC handler（§8.2）。
package dashboard

import (
	"context"
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
	// priceTopic 实时现价广播（D-049）：PersonalMonitor 经 onPrice 发布 → StreamUpdates 扇出 case 7。nil = 未接线（测试直接构造 Service）。
	priceTopic *bus.Topic[position.PositionPrice]
	// oppTopic 新机会推送广播（D-050）：engine 新插入发布 → StreamUpdates 扇出 case 2。nil = 未接线（测试直接构造 Service）。
	oppTopic *bus.Topic[pgstore.Opportunity]
	// fundingNeedTopic 资金注入提醒广播（D-072）：engine 余额不足发布 → StreamUpdates 扇出 case 10。nil = 未接线（测试直接构造 Service）。
	fundingNeedTopic *bus.Topic[FundingNeedNotice]
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
	watchlistMu       sync.RWMutex
	watchlistSet      map[string]struct{}
	watchlistInitOnce bool // true = 已查库初始化过（空表也置 true，避免重复查）
	// quoteFlushInterval StreamUpdates case 1 蓄压 flush 间隔（默认 1s，测试注 10ms）。
	quoteFlushInterval time.Duration
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
// priceTopic 为实时现价广播 topic（D-049，传 main 创建的 priceTopic）；
// oppTopic 为新机会站内推送 topic（D-050，传 main 创建的 oppTopic）；
// bpm 为 Broker 实持仓 monitor（D-052，传 main 创建的 bpm）；
// notionalUSD 为 net_usd 换算的单笔名义（D-053，传 config.OpportunityNotionalUSD）；
// acctMon 为账户资产 monitor（D-053，传 main 创建的 acctMon）。
func New(store *pgstore.Store, qbus *bus.QuoteBus, pipeline *exec.Pipeline, bm *BrokerManager, reviewer func(), freshTTL time.Duration, liqMMR float64, fundingDropRatio float64, swapArbEquityUseRatio float64, priceTopic *bus.Topic[position.PositionPrice], oppTopic *bus.Topic[pgstore.Opportunity], bpm *BrokerPositionMonitor, notionalUSD float64, acctMon *AccountMonitor) *Service {
	return &Service{store: store, qbus: qbus, pipeline: pipeline, bm: bm, reviewer: reviewer, freshTTL: freshTTL, liqMMR: liqMMR, fundingDropRatio: fundingDropRatio, swapArbEquityUseRatio: swapArbEquityUseRatio, priceTopic: priceTopic, oppTopic: oppTopic, bpm: bpm, notionalUSD: notionalUSD, acctMon: acctMon}
}

// Store 返回底层 store（D-070 autotrade engine 用）。
func (s *Service) Store() *pgstore.Store { return s.store }

// AutoClosePosition 平仓（D-070 autotrade engine 自动平仓用，区别于 RPC ClosePosition）。
func (s *Service) AutoClosePosition(ctx context.Context, posID int64) error {
	return s.pipeline.ClosePosition(ctx, posID)
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
