package autotrade

import (
	"context"
	"log"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
	"arb/internal/exec"
	"arb/internal/settlement"
	"arb/internal/store/pgstore"
)

// ZeroSettlementDetector D-182 S1：零结算检测接口（scanAndClose 周期调用）。
// dashboard.SettlementProfileCollector 实现此接口。
type ZeroSettlementDetector interface {
	RecordZeroForPosition(ctx context.Context, posID int64)
}

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

	// D-159 W1a R1 + D-168 自动准入：SWAPARB 组合门禁。
	// SwapArbAllowCombos 空切片 = RUNNING 下 fail-closed（autoAllow=false 时）。
	// autoAllow=true 时空白名单=自动放行所有通过门禁栈的组合。
	// SwapArbDenyCombos 黑名单（D-168）：显式禁止已知问题组合，优先级最高。
	// SwapArbMaxGrossExposure 全局活跃组合名义合计上限（>0 才校验）。
	// MaxActiveCombos 同组合最大并发数，默认 1（D-138 单组合口径）。
	SwapArbAllowCombos      []exec.ComboAllowSpec
	SwapArbDenyCombos       []exec.ComboAllowSpec
	SwapArbAutoAllow        bool
	SwapArbMaxGrossExposure float64
	MaxActiveCombos         int

	// D-159 W1a I6：environment/is_real 一致性检查。
	// Environment 执行环境固定值（'demo'/'live'）。开仓前校验 venue 的 is_real 与此值一致。
	// 空 = 未配置（不检查，回退旧行为）。
	Environment string
	// D-159 W6a S2：LIVE 人工开关（ARB_LIVE_TRADE，默认 false）。
	// is_real=true 账户 + LiveTrade=false → 拒绝开仓（Gate.Submit + 手动 RPC 同源）。
	// is_real=false（demo）不受限制。切换=改 env+重启+审计日志（无 RPC）。
	LiveTrade bool
	// D-165 S1：swap 偏差拦截阈值（默认 50.0）。同 venue 已有 OPEN 持仓偏差超阈值→拒绝新开仓。
	SwapDeviationGatePct float64
	// D-165 S2：swap 偏差退出阈值（默认 50.0）。recalcNetSwap 对拍偏差超阈值→用 broker 实际值。
	SwapDeviationExitPct float64
	// D-165 S3：swap facts 零值校验开关（默认 true）。facts=0 但 broker 有非零 swap 历史→拒绝。
	SwapFactsZeroCheckEnabled bool
	// D-165.1 S1：leg contract size resolver（MT 腿 CS≠1 时 notional 换算用）。
	// nil = 未注入（回退 CS=1，CEX 腿正确，MT 腿 notional 少算但不阻塞）。
	LegContractSizeResolver func(venueKey, venueSymbol string) (float64, bool)

	// D-072 §3.3/§4.2 swap/funding 退出参数
	MinNetSwapBps  float64 // discoverSwapCombos 最低净 swap（ARB_AUTOTRADE_MIN_NET_SWAP_BPS，默认 0.2）
	SwapArbExitBps float64 // SWAPARB/CARRY 退出净 swap bps（ARB_SWAPARB_EXIT_NET_BPS，默认 0）
	FundingExitPct float64 // FUNDING_HEDGE/PERP_FUNDING 退出净年化（ARB_FUNDING_EXIT_NET_PCT，默认 0）

	// D-133 T1：PERP_FUNDING 平仓规则重构——持有下限 + 连续去抖
	PerpFundMinHold      time.Duration // ARB_PERPFUNDING_MIN_HOLD 默认 8h；0=不限
	PerpFundExitDebounce int           // ARB_PERPFUNDING_EXIT_DEBOUNCE 默认 3；0/1=单次即平

	// D-077 修复⑪B N6：入场成本容忍+陈旧度 env 化
	EntrySpreadMinBps float64       // SWAPARB 入场 spreadBps 下限（ARB_SWAPARB_ENTRY_SPREAD_MIN_BPS，默认 -3）
	FreshnessTTL      time.Duration // 报价陈旧排除（复用 ARB_DETECTOR_FRESHNESS_TTL，默认 120s）
	// D-159 W4a R7：单腿 bid-ask spread 宽度上限（bps），>0 才校验，0=不校验（ARB_SWAPARB_MAX_LEG_SPREAD_BPS，默认 50）。
	SwapArbMaxLegSpreadBps float64 // 任一腿 spread 过宽=流动性异常/报价不一致 → skip

	// D-169 P0：入场价差/基差上限（bps），>0 才校验，0=不校验。
	EntrySpreadMaxBps float64 // ARB_SWAPARB_ENTRY_SPREAD_MAX_BPS（默认 30）
	EntryBasisMaxBps  float64 // ARB_SWAPARB_ENTRY_BASIS_MAX_BPS（默认 30）

	// D-180 S3：基差止损阈值（bps），PnL_bps < -BasisStopBps → 平仓（默认 4）。
	BasisStopBps float64
	// D-180 S4：入场门禁——净息差厚度下限（bps/日，spec 源×折扣后比较；默认 1.0）。
	MinNetCarryBps float64
	// D-180 S4：spec 源折扣系数（冷启动不裸信规格；默认 0.5）。仅作用入场门禁。
	SpecDiscount float64
	// D-180 S4：回收期上限（天，2×spreadBps/净息差 ≤ 此值才放行；默认 30）。
	MaxPaybackDays int
	// D-180 S4：集中度上限（同 venue×symbol 活跃腿数 < 此值才放行；默认 2）。
	MaxLegsPerSymbol int

	// D-104 P0：swap facts 新鲜度门禁（fail-closed，ARB_SWAP_FACT_MAX_AGE，默认 2h）。
	// discoverSwapCombos 使用 lf/sf 前查 now−f.Ts > SwapFactMaxAge → 跳过该 fact + 计数日志。
	SwapFactMaxAge time.Duration

	// B：组合平仓后重入冷却（ARB_SWAPARB_REENTRY_COOLDOWN_DAYS，默认 7）
	ReentryCooldownDays int

	// D-107 P4：事件触发扫描节流窗口（ARB_SCAN_THROTTLE，默认 30s）。
	// Kick() 触发 scanIfDue：窗口内丢弃、超窗扫。兜底 10min 保证有界延迟。
	ScanThrottle time.Duration

	// D-142 ④：组合保证金预算观察版告警（只告警不拦截）。
	// 双腿合计保证金 / min(equity) > 此值 → warn 告警。0 = 不启用。
	// 杠杆不对称（两腿 leverage 差异显著）→ info 提示。
	SwapArbMarginBudgetPct float64
}

// Engine 自动交易引擎：三 loop → gate → 执行 → 自动平仓 → 策略淘汰（D-070/D-072）。
type Engine struct {
	cfg      Config
	store    *pgstore.Store
	service  *dashboard.Service
	oppTopic *bus.Topic[pgstore.Opportunity]
	quotes   *bus.QuoteBus // 实时报价（D-070 均值回归平仓用）
	gate     *Gate         // 统一开仓门禁（D-072）

	// D-182 S1：零结算检测器（scanAndClose 周期对活跃仓检测恒零 → settled=0）。
	// nil = 未注入（跳过零检测，向后兼容）。
	zeroDetector ZeroSettlementDetector

	firstTickDone bool // 启动首跑淘汰标记

	// fundingNeedSent 资金注入提醒节流（D-072）：key=SWAPARB:canonical:venueLong:venueShort → YYYY-MM-DD。
	// 每日每组合至多 1 条（内存，重启重置——实时弹窗语义，不持久化）。
	fundingNeedSent map[string]string

	// D-133 T1：PERP_FUNDING 退出连续去抖计数——key=posID → 连续 breach 次数。
	// 读写都在 auto-close 轮询 goroutine（单写者），mu 为防御性（未来并发扩展兜底）。
	// 平仓/读数回正/floor 未满 → 删除条目（防泄漏）。重启丢失=重新去抖（无害，10min 轮询）。
	fundingBreach map[int64]int
	fundingMu     sync.Mutex

	// D-087 §3.2：settlement.Settle 依赖 + 淘汰判定函数（main 装配后调 SetSettleDeps）。
	settleDeps settlement.Deps
	eliminator settlement.Eliminator

	// D-107 P4：事件触发扫描——kickCh buffered(1) 非阻塞 send（合并语义同 P2 监控）；
	// lastScan 节流基准（仅 positionLoop goroutine 读写，无锁——quoteReconIdx 单协程先例）。
	kickCh   chan struct{}
	lastScan time.Time

	// D-140 S2：CS-miss 聚合告警——按 (venue,canonical) 计数，定期 flush info 告警。
	csMissCounts map[string]int       // key=venue|canonical → 累计 miss 次数
	csMissSent   map[string]time.Time // key → 最近一次告警时间（1h 去重）
	csMissMu     sync.Mutex
	AlertSink    func(level, msg string) // nil = 未注入（no-op）

	// D-144 ②：保证金告警 per-key 去重（canonical:vl:vs:type → 最近一次告警时间，1h 窗口）。
	// 同 mt-health lastCritAlert 模式；内存，重启重置（实时告警语义，不持久化）。
	marginAlertSent map[string]time.Time
	marginAlertMu   sync.Mutex
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
		fundingBreach:   make(map[int64]int),  // D-133 T1：PERP_FUNDING 去抖计数
		csMissCounts:    make(map[string]int), // D-140 S2：CS-miss 聚合
		csMissSent:      make(map[string]time.Time),
		marginAlertSent: make(map[string]time.Time), // D-144 ②：保证金告警去重
		kickCh:          make(chan struct{}, 1),     // D-107 P4：buffered(1) 非阻塞 send = 事件合并
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

// SetZeroSettlementDetector D-182 S1：注入零结算检测器（scanAndClose 周期调用）。
func (e *Engine) SetZeroSettlementDetector(d ZeroSettlementDetector) {
	e.zeroDetector = d
}

// Run 启动自动交易引擎（阻塞，ctx 取消时退出）。
func (e *Engine) Run(ctx context.Context) {
	if !e.cfg.Enabled {
		log.Println("[autotrade] disabled (ARB_AUTOTRADE_ENABLED=false)")
		return
	}
	e.ValidateStartup()
	log.Printf("[autotrade] enabled: maxNotional=$%.0f maxPos=%d maxDailyLoss=$%.0f minNetBps=%.1f scanThrottle=%s fallback=%s",
		e.cfg.MaxNotional, e.cfg.MaxPositions, e.cfg.MaxDailyLoss, e.cfg.MinNetBps, e.cfg.ScanThrottle, positionFallbackInterval)

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

// positionFallbackInterval D-107 P4：低频兜底扫描间隔（10min，防网关事件丢失窗口）。
// 包级 var 测试可覆写（marginReconnectBackoff 先例）。
var positionFallbackInterval = 10 * time.Minute

// Kick D-107 P4：orderTopic 事件触发重扫（非阻塞 send 到 buffered(1) chan——合并语义同 P2 监控）。
func (e *Engine) Kick() {
	select {
	case e.kickCh <- struct{}{}:
	default: // 已有待处理 kick，合并丢弃
	}
}

// scanIfDue D-107 P4：kick 触发节流扫描——距上次扫描 < ScanThrottle → 丢弃（兜底 10min 保证有界延迟）；
// 否则执行 scanAndClose + stateMachineTick + 更新 lastScan。
// lastScan 无锁：仅 positionLoop goroutine 读写（quoteReconIdx 单协程先例）。
func (e *Engine) scanIfDue(ctx context.Context) {
	if time.Since(e.lastScan) < e.cfg.ScanThrottle {
		return // 节流窗口内丢弃
	}
	e.scanAndClose(ctx)
	e.stateMachineTick(ctx)
	e.lastScan = time.Now()
}

// positionLoop D-107 P4：启动即扫 → select 三支（kickCh→scanIfDue / fallback→直扫 / ctx.Done）。
// 事件驱动优先（orderTopic → Kick → scanIfDue 30s 节流），fallback 10min 兜底（防事件丢失窗口）。
func (e *Engine) positionLoop(ctx context.Context) {
	// 启动即扫（首个扫描立即可用 + 设 lastScan 节流基准）。
	e.scanAndClose(ctx)
	e.stateMachineTick(ctx)
	e.lastScan = time.Now()

	fallback := time.NewTicker(positionFallbackInterval)
	defer fallback.Stop()
	csMissTick := time.NewTicker(csMissAlertInterval) // D-140 S2：CS-miss 聚合告警
	defer csMissTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.kickCh:
			e.scanIfDue(ctx) // D-107 P4：事件触发节流扫描
		case <-fallback.C:
			// D-107 P4：兜底直扫（不受节流限制——10min 有界延迟保证）。
			e.scanAndClose(ctx)
			e.stateMachineTick(ctx)
			e.lastScan = time.Now()
		case <-csMissTick.C:
			e.flushCSMissAlerts() // D-140 S2
		}
	}
}
