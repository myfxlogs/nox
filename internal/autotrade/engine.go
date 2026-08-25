package autotrade

import (
	"context"
	"log"
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
	"arb/internal/settlement"
	"arb/internal/store/pgstore"
)

// Config 自动交易引擎配置（D-070/D-072）。
type Config struct {
	Enabled      bool
	MaxNotional  float64
	MaxPositions int
	MaxDailyLoss float64
	MinNetBps    float64
	CloseBpsThr  float64       // 收敛型平仓阈值（expected_net_bps <= 此值则平仓）
	PollInterval time.Duration // OPEN 持仓扫描间隔
	NotionalUSD  float64       // 单笔名义额（用于算 qty）

	// D-072 §3.4 硬编码外部化（默认值=现行为，零迁移）
	AllowVenues       map[string]bool    // handleOpportunity venue 白名单 + MR venueA/B
	AllowCanonicals   map[string]bool    // handleOpportunity symbol 白名单
	ExcludeCanonicals map[string]bool    // BRENT/WTI 排除
	MeanRevertThresh  map[string]float64 // MR 阈值 map

	// D-072 §3.3/§4.2 swap/funding 退出参数
	MinNetSwapBps  float64 // discoverSwapCombos 最低净 swap（ARB_AUTOTRADE_MIN_NET_SWAP_BPS，默认 0.2）
	SwapArbExitBps float64 // SWAPARB/CARRY 退出净 swap bps（ARB_SWAPARB_EXIT_NET_BPS，默认 0）
	FundingExitPct float64 // FUNDING_HEDGE/PERP_FUNDING 退出净年化（ARB_FUNDING_EXIT_NET_PCT，默认 0）

	// D-077 修复⑪B N6：入场成本容忍+陈旧度 env 化
	EntrySpreadMinBps float64       // SWAPARB 入场 spreadBps 下限（ARB_SWAPARB_ENTRY_SPREAD_MIN_BPS，默认 -3）
	FreshnessTTL      time.Duration // 报价陈旧排除（复用 ARB_DETECTOR_FRESHNESS_TTL，默认 120s）

	// D-104 P0：swap facts 新鲜度门禁（fail-closed，ARB_SWAP_FACT_MAX_AGE，默认 2h）。
	// discoverSwapCombos 使用 lf/sf 前查 now−f.Ts > SwapFactMaxAge → 跳过该 fact + 计数日志。
	SwapFactMaxAge time.Duration

	// B：组合平仓后重入冷却（ARB_SWAPARB_REENTRY_COOLDOWN_DAYS，默认 7）
	ReentryCooldownDays int
}

// Engine 自动交易引擎：三 loop → gate → 执行 → 自动平仓 → 策略淘汰（D-070/D-072）。
type Engine struct {
	cfg      Config
	store    *pgstore.Store
	service  *dashboard.Service
	oppTopic *bus.Topic[pgstore.Opportunity]
	quotes   *bus.QuoteBus // 实时报价（D-070 均值回归平仓用）
	gate     *Gate         // 统一开仓门禁（D-072）

	firstTickDone bool // 启动首跑淘汰标记

	// fundingNeedSent 资金注入提醒节流（D-072）：key=SWAPARB:canonical:venueLong:venueShort → YYYY-MM-DD。
	// 每日每组合至多 1 条（内存，重启重置——实时弹窗语义，不持久化）。
	fundingNeedSent map[string]string

	// D-087 §3.2：settlement.Settle 依赖 + 淘汰判定函数（main 装配后调 SetSettleDeps）。
	settleDeps settlement.Deps
	eliminator settlement.Eliminator
}

// New 创建自动交易引擎。
func New(cfg Config, store *pgstore.Store, service *dashboard.Service, oppTopic *bus.Topic[pgstore.Opportunity], quotes *bus.QuoteBus) *Engine {
	return &Engine{
		cfg:             cfg,
		store:           store,
		service:         service,
		oppTopic:        oppTopic,
		quotes:          quotes,
		gate:            NewGate(store, cfg),
		fundingNeedSent: make(map[string]string),
	}
}

// ValidateStartup 启动 fail-fast 校验（D-072 实施修正②）。
// OpportunityNotionalUSD > AutoTradeMaxNotional → log.Fatal（SWAPARB 不参与）。
func (e *Engine) ValidateStartup() {
	if e.cfg.NotionalUSD > e.cfg.MaxNotional && e.cfg.MaxNotional > 0 {
		log.Fatalf("[autotrade] config fail-fast: OpportunityNotionalUSD $%.0f > AutoTradeMaxNotional $%.0f (fix env or lower ARB_OPP_NOTIONAL_USD)", e.cfg.NotionalUSD, e.cfg.MaxNotional)
	}
}

// SetSettleDeps 注入结算依赖 + 淘汰判定函数（D-087 §3.2，main 装配后调）。
func (e *Engine) SetSettleDeps(deps settlement.Deps, elim settlement.Eliminator) {
	e.settleDeps = deps
	e.eliminator = elim
}

// Run 启动自动交易引擎（阻塞，ctx 取消时退出）。
func (e *Engine) Run(ctx context.Context) {
	if !e.cfg.Enabled {
		log.Println("[autotrade] disabled (ARB_AUTOTRADE_ENABLED=false)")
		return
	}
	e.ValidateStartup()
	log.Printf("[autotrade] enabled: maxNotional=$%.0f maxPos=%d maxDailyLoss=$%.0f minNetBps=%.1f poll=%s",
		e.cfg.MaxNotional, e.cfg.MaxPositions, e.cfg.MaxDailyLoss, e.cfg.MinNetBps, e.cfg.PollInterval)

	// 1. 机会订阅循环（detector 产生的 CROSS_VENUE 机会）
	go e.oppLoop(ctx)

	// 2. 均值回归扫描循环（D-070：绕过 detector，直接监控价差）
	go e.meanRevertLoop(ctx)

	// 3. swap 套利循环（D-070：持有赚 swap）
	go e.swapArbLoop(ctx)

	// 4. 持仓扫描循环（自动平仓 + 状态机 tick）
	e.positionLoop(ctx)
}

// maxPositionsReached H4.1 满仓短路：CountOpenPositions >= MaxPositions → true（整轮跳过扫描）。
// 查询失败 → false（fail-open，让 gate 兜底拒绝，不阻断扫描——gate 是最终防线）。
// MaxPositions <= 0 → false（未配置上限，不短路）。
func (e *Engine) maxPositionsReached(ctx context.Context) bool {
	if e.cfg.MaxPositions <= 0 {
		return false
	}
	n, err := e.store.CountOpenPositions(ctx)
	if err != nil {
		// fail-open：查询失败不阻断扫描，gate 兜底（gate 已 fail-closed 拒绝）
		return false
	}
	return maxPositionsReachedDecision(n, e.cfg.MaxPositions)
}

// maxPositionsReachedDecision H4.1 纯函数：openCount >= maxPositions → true（满仓短路）。
// 抽出供单测（满仓时零 intent 的判定逻辑，无 IO）。
func maxPositionsReachedDecision(openCount, maxPositions int) bool {
	return openCount >= maxPositions
}

// positionLoop 定期扫描 OPEN 持仓 → 按 type 分派退出 + 状态机 tick（D-072）。
func (e *Engine) positionLoop(ctx context.Context) {
	ticker := time.NewTicker(e.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.scanAndClose(ctx)
			e.stateMachineTick(ctx)
		}
	}
}

// scanAndClose 扫描 OPEN 持仓，按 type 分派退出条件（D-072 §4.2）。
func (e *Engine) scanAndClose(ctx context.Context) {
	positions, err := e.store.ListPositions(ctx, "OPEN", 50, 0)
	if err != nil {
		log.Printf("[autotrade] list open positions: %v", err)
		return
	}
	for _, p := range positions {
		shouldClose, reason := e.checkExitCondition(ctx, &p)
		if !shouldClose {
			continue
		}
		log.Printf("[autotrade] auto-close pos=%d type=%s: %s", p.ID, p.Type, reason)
		if err := e.service.AutoClosePosition(ctx, p.ID); err != nil {
			log.Printf("[autotrade] auto-close pos=%d failed: %v", p.ID, err)
			continue
		}
		// D-087 §3.2：settlement.Settle 替换 updateStrategyStat（旧对象 no-op 缺陷根除）
		if e.settleDeps != nil {
			if err := settlement.Settle(ctx, e.settleDeps, p.ID, e.eliminator); err != nil {
				log.Printf("[autotrade] settle pos=%d: %v", p.ID, err)
			}
		}
	}
}
