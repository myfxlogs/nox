package autotrade

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"arb/internal/bus"
	"arb/internal/errclass"
	"arb/internal/exec"
	"arb/internal/safego"
	"arb/internal/settlement"
	"arb/internal/store/pgstore"
)

// ZeroSettlementDetector D-182 S1：零结算检测接口（scanAndClose 周期调用）。
// dashboard.SettlementProfileCollector 实现此接口。
type ZeroSettlementDetector interface {
	RecordZeroForPosition(ctx context.Context, posID int64)
}

// Config 已提取至 engine_config.go（D-261 S2 纯搬家拆分）。

// Engine 自动交易引擎：三 loop → gate → 执行 → 自动平仓 → 策略淘汰（D-070/D-072）。
type Engine struct {
	cfg   Config
	store *pgstore.Store
	// D-403 S3：XASSET 白名单热重载读点（atomic 指针整体替换；初值=启动加载结果，
	// approve RPC 后经 ReloadXAssetAllowCombos 免重启生效）。读侧 handleOpportunity
	// 高频 vs 写侧 approve 低频——atomic Load/Store 无锁竞态安全（S6 race ×3 强制）。
	xassetAllow atomic.Pointer[[]exec.XAssetComboAllowSpec]
	// D-515 S5：swaparb_allow_combos 表热读快照（allow/deny 双轨整体替换；nil→cfg 启动值回退）。
	swapArb atomic.Pointer[swapArbComboSnapshot]
	// D-443 S2：venue/canonical 执行白名单热读 holder（60s 周期重载免重启，同 xassetAllow 模式）。
	allowRules atomic.Pointer[allowRules]
	// D-475a S2：fin_venue_symbol_flags 执行层投影（同 allowRules 原子快照模式；
	// nil=空集放行——旗标缺席=无风险信号，与 allowRules 空集全拒故意不对称）。
	flags    atomic.Pointer[flagSnapshot]
	service  engineService // D-508 S7b：窄接口注入，解 *dashboard.Service 逆向绑定
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
	fundingBreach  map[int64]int
	disasterBreach map[int64]int // D-378b S1：灾难带去抖计数
	disasterMu     sync.Mutex    // D-378b：disasterBreach 锁（独立于 fundingMu——异判定域不互斥）
	fundingMu      sync.Mutex

	// D-495 S3：腿可平性 suspend 转场去重——key=posID，值=当前是否处于 suspend。
	// 只在 suspend 进/出转场各记一条 warn（关市是常态非异常，恢复后同型免再报）。
	legSuspend   map[int64]bool
	legSuspendMu sync.Mutex

	// D-087 §3.2：settlement.Settle 依赖 + 淘汰判定函数（main 装配后调 SetSettleDeps）。
	settleDeps settlement.Deps
	eliminator settlement.Eliminator
	// D-256-A S2：风险族解析器（CARRY/SWAPARB 合并淘汰），main 装配后调 SetSettleDeps 注入。
	familyResolver settlement.FamilyResolver

	// D-107 P4：事件触发扫描——kickCh buffered(1) 非阻塞 send（合并语义同 P2 监控）；
	// lastScan 节流基准（仅 positionLoop goroutine 读写，无锁——quoteReconIdx 单协程先例）。
	kickCh   chan struct{}
	lastScan time.Time

	// D-469c S1/S3：评估节拍事件化——mrKick=meanRevert 入场扫描 kick（独立 chan：
	// 与 positionLoop 退出扫描语义不混）；factsKick=swapArb facts 写后 kick。
	mrKick     chan struct{}
	factsKick  chan struct{}
	lastMRScan time.Time // mrScanMinInterval 节流基准（仅 meanRevertLoop goroutine 读写）

	// D-140 S2：CS-miss 聚合告警——按 (venue,canonical) 计数，定期 flush info 告警。
	csMissCounts map[string]int       // key=venue|canonical → 累计 miss 次数
	csMissSent   map[string]time.Time // key → 最近一次告警时间（1h 去重）
	csMissMu     sync.Mutex
	AlertSink    func(level, msg string) // nil = 未注入（no-op）

	// D-144 ②：保证金告警 per-key 去重（canonical:vl:vs:type → 最近一次告警时间，1h 窗口）。
	// 同 mt-health lastCritAlert 模式；内存，重启重置（实时告警语义，不持久化）。
	marginAlertSent map[string]time.Time
	marginAlertMu   sync.Mutex

	// D-256-A S5：复活扳机告警 per-key 去重（ComboKey → 最近告警时间，1h 窗口）。
	// 同 marginAlertSent 模式；内存，重启重置（实时告警语义，不持久化）。
	revivalAlertSent map[string]time.Time

	// D-256-A S1：non-settle venue bare 集合（lazy 构建一次，swapRateFor 入口 guard）。
	// 条目来自 cfg.SwapNonSettleVenues（格式 "277842155:mt5"），剥离后缀存 bare "277842155"。
	nonSettleOnce sync.Once
	nonSettleBare map[string]bool
}

// New 已提取至 engine_new.go（D-195-C1 拆分避免超 300 行）。

// ValidateStartup 启动 fail-fast 校验（D-072 实施修正②）。
// OpportunityNotionalUSD > AutoTradeMaxNotional → log.Fatal（SWAPARB 不参与）。
func (e *Engine) ValidateStartup() {
	if e.cfg.NotionalUSD > e.cfg.MaxNotional && e.cfg.MaxNotional > 0 {
		log.Fatalf("[autotrade] config fail-fast: OpportunityNotionalUSD $%.0f > AutoTradeMaxNotional $%.0f (fix env or lower ARB_OPP_NOTIONAL_USD)", e.cfg.NotionalUSD, e.cfg.MaxNotional)
	}
}

// SetSettleDeps 注入结算依赖 + 淘汰判定函数（D-087 §3.2，main 装配后调）。
// D-256-A S2：family 非 nil 时淘汰聚合改用族级（CARRY/SWAPARB 合并样本）。
func (e *Engine) SetSettleDeps(deps settlement.Deps, elim settlement.Eliminator, family settlement.FamilyResolver) {
	e.settleDeps = deps
	e.eliminator = elim
	e.familyResolver = family
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
	safego.Go("engine:oppLoop", func() { e.oppLoop(ctx) })

	// 2. 均值回归扫描循环（D-070：绕过 detector，直接监控价差）
	safego.Go("engine:meanRevertLoop", func() { e.meanRevertLoop(ctx) })

	// 3. swap 套利循环（D-070：持有赚 swap）
	safego.Go("engine:swapArbLoop", func() { e.swapArbLoop(ctx) })

	// D-469c S1/S2：quote 事件分发——单订阅扇出两 kick chan（非阻塞 send 防洪峰；
	// mr 过滤=MeanRevertThresh canonical 集，position kick 全订走 scanIfDue 节流合流）。
	if e.quotes != nil {
		safego.Go("engine:quoteKickDispatch", func() { e.quoteKickDispatch(ctx) })
	}

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

// positionFallbackInterval D-107 P4：低频兜底扫描间隔旧值（10min）。
// D-378a S3 起默认 60s 配置化（ARB_POSITION_EVAL_SEC→cfg.PositionFallbackInterval）；
// 本常量保留为 ≤0 防呆回退值。包级 var 测试可覆写（marginReconnectBackoff 先例）。
var positionFallbackInterval = 10 * time.Minute

// fallbackInterval D-378a S3：评估节拍——修复评估饥荒（无订单事件时退出评估最多饿 10min，
// 「T 分钟持续」类判据无定义域）。scanAndClose 全仓扫描（≤50 仓×内存 quote 读）60s 成本可忽略；
// 顺带治愈 SWAPARB 等其他型同款饥荒。事件驱动路径（orderTopic→kick→30s 节流）逐字节不动。
func (e *Engine) fallbackInterval() time.Duration {
	if e.cfg.PositionFallbackInterval > 0 {
		return e.cfg.PositionFallbackInterval
	}
	return positionFallbackInterval
}

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

	fallback := time.NewTicker(e.fallbackInterval()) // D-378a S3：节拍配置化（D-562 注记：事件驱动 kick 主路+本 ticker=评估饥荒兜底节拍，退出判定消费 qbus 实时报价非轮询快照——判定证据实时合规）
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

// quoteKickDispatch D-469c S1/S2：qbus 全订 → 每帧非阻塞 send e.kickCh（positionLoop
// scanIfDue 30s 节流合流）；canonical ∈ MeanRevertThresh 时另 send mrKick。
func (e *Engine) quoteKickDispatch(ctx context.Context) {
	ch, cancel := e.quotes.Subscribe("")
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case q, ok := <-ch:
			if !ok {
				return
			}
			e.Kick() // S2：positionLoop quote-kick（节流在 scanIfDue 侧）
			if _, tracked := e.cfg.MeanRevertThresh[q.Symbol]; tracked {
				e.KickMeanRevert() // S1：meanRevert 入场扫描 kick
			}
		}
	}
}

// KickMeanRevert D-469c S1：quote 帧（MeanRevertThresh 覆盖 canonical）→ mrKick 非阻塞合并。
func (e *Engine) KickMeanRevert() {
	select {
	case e.mrKick <- struct{}{}:
	default:
	}
}

// KickSwapArb D-469c S3：facts 写后（mt_swap/funding 三源）→ factsKick 非阻塞合并。
func (e *Engine) KickSwapArb() {
	select {
	case e.factsKick <- struct{}{}:
	default:
	}
}

// BreakerSnapshot D-560 S3：拒单熔断只读快照透出（dashboard pending/combos 三字段源）。
// key=dedupKey|class；返回在活动窗条目。
func (e *Engine) BreakerSnapshot() map[string]errclass.RejectBreakerInfo {
	return e.gate.BreakerSnapshot()
}

// ResumeBreaker D-560 S3：ResumeXAssetCombo 行为=清该 dedupKey 全部熔断态。
func (e *Engine) ResumeBreaker(dedupKey string) int {
	return e.gate.ResumeBreaker(dedupKey)
}

// dedupCapHalf D-508 S5：告警去重集无界 map 上限（fundingNeedSent/marginAlertSent/
// revivalAlertSent/nonSettleBare）——满 10k 任删一半。去重语义弱保证：误删最坏=
// 同告警提前重发一次（内存去重，重启本就重置），可接受。
const alertDedupCap = 10000

func dedupCapHalf[T any](m map[string]T) {
	if len(m) < alertDedupCap {
		return
	}
	n := 0
	for k := range m {
		if n >= alertDedupCap/2 {
			break
		}
		delete(m, k)
		n++
	}
}
