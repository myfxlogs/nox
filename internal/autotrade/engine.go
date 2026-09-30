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

	// tradeModeProber D-004 P1#2：按 venueKey 取 MT5 trade_mode 即时探针
	// （装配面注入，nil=未接线——平仓闸侧 checkTradeMode 放行+日志）。
	tradeModeProber func(venueKey string) exec.TradeModeProber

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

	// D-107 P4→D-004 P1#4：kickCh=订单事件触发权威全扫（合流不丢事件，见
	// positionLoop kickCoalesce）；quoteCh=positionLoop quote 快路帧（buffered 256
	// 满则丢帧——下一帧还会来，报价热路径不阻塞）；carryKick=carry facts 写后 kick。
	kickCh    chan struct{}
	quoteCh   chan bus.Quote
	carryKick chan struct{}

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

	// 测试缝（D-004 P1#4 分层可观测）：scanHook=权威全扫入口，evalHook=单仓权威评估
	// 入口（posID）。nil=未注入；生产装配不接。
	scanHook func()
	evalHook func(posID int64)

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

// SetTradeModeProberResolver D-004 P1#2：注入按 venueKey 取执行时 trade_mode
// 即时探针的闭包（装配面接 exec.Registry）；nil/缺席 → MT 腿平仓侧放行+日志
// （平仓减风险，venue 拒单为最终权威；判据本体在 exec.checkTradeMode）。
func (e *Engine) SetTradeModeProberResolver(resolve func(venueKey string) exec.TradeModeProber) {
	e.tradeModeProber = resolve
}

// Run 启动自动交易引擎（阻塞，ctx 取消时退出）。
func (e *Engine) Run(ctx context.Context) {
	if !e.cfg.Enabled {
		log.Println("[autotrade] disabled (ARB_AUTOTRADE_ENABLED=false)")
		return
	}
	e.ValidateStartup()
	log.Printf("[autotrade] enabled: maxNotional=$%.0f maxPos=%d maxDailyLoss=$%.0f minNetBps=%.1f kickCoalesce=%s watchdog=%s",
		e.cfg.MaxNotional, e.cfg.MaxPositions, e.cfg.MaxDailyLoss, e.cfg.MinNetBps, kickCoalesce, e.fallbackInterval())

	// 1. 机会订阅循环（detector 产生的 CROSS_VENUE 机会）
	safego.Go("engine:oppLoop", func() { e.oppLoop(ctx) })

	// 2. 均值回归扫描循环（D-070：绕过 detector，直接监控价差）
	safego.Go("engine:meanRevertLoop", func() { e.meanRevertLoop(ctx) })

	// 3. swap 套利循环（D-070：持有赚 swap）
	safego.Go("engine:swapArbLoop", func() { e.swapArbLoop(ctx) })

	// D-469c S1/S2：quote 事件分发——单订阅扇出两 kick chan（非阻塞 send 防洪峰；
	// mr 过滤=MeanRevertThresh canonical 集，position 侧全订走 quoteCh 快路）。
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

// KickCarryEval D-004 P1#4：carry 退出判据输入（mt_swap/三所 funding facts）写后
// → positionLoop 权威全扫（息差型净收入变了）。buffered(1) 非阻塞合并。
func (e *Engine) KickCarryEval() {
	select {
	case e.carryKick <- struct{}{}:
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
