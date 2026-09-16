// Package exec 的 pipeline.go 实现执行管线（§13.1/§13.3）。
// ExecuteOpportunity: 复核 → 建 position(OPENING) → 双腿并发下单 → 回填状态。
// ClosePosition: 复核 OPEN → 双腿反向平仓 → 回填 realized_pnl → CLOSED。
package exec

import (
	"context"
	"errors"
	"sync"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// RiskLimits 风控上限（§12 executor 配置）。
type RiskLimits struct {
	MaxNotionalUSD     float64
	MaxOpenPositions   int
	MaxLegImbalancePct float64
}

// 风控拒单 sentinel error（D-058）。ExecuteOpportunity RPC 据此映射 FAILED+message，非 CodeInternal。
var (
	// ErrMaxOpenPositions 持仓数达上限，拒绝新开仓。
	ErrMaxOpenPositions = errors.New("max open positions reached")
	// ErrDuplicateExecution 该机会已有未平仓持仓，拒绝重复执行（幂等防重）。
	ErrDuplicateExecution = errors.New("opportunity already has an open position")
	// ErrEmptyPositionType 持仓类型为空，拒绝建仓（修复⑭ S4：空 type 撞 positions_type_check，
	// 生产数据完整性隐患——空 type 持仓本不该被创建）。
	ErrEmptyPositionType = errors.New("position type required (empty type would violate positions_type_check)")
	// ErrEmptyLegs legs 为空，拒绝建仓（修复⑭ S4：nil legs 无下单意图，不应落库）。
	ErrEmptyLegs = errors.New("legs required (nil/empty legs has no execution intent)")
	// D-159 W1a R1 S2.3：SWAPARB 精确组合门禁错误。
	ErrComboNotAllowed       = errors.New("swaparb combo not in allowlist")
	ErrXAssetComboNotAllowed = errors.New("xasset combo not in allowlist") // D-311 S6
	ErrQtyExceedsLimit       = errors.New("swaparb leg qty exceeds allowlist limit")
	ErrGrossExposureExceeds  = errors.New("swaparb gross exposure exceeds allowlist limit")
)

// Pipeline 是执行管线（§13.1）。
type Pipeline struct {
	registry *Registry
	store    *pgstore.Store
	qbus     *bus.QuoteBus
	limits   RiskLimits
	// verifier 平仓后 ticket 回读（D-077 修复⑩ G1）。nil = 未接线（测试，跳过回读）。
	verifier PositionVerifier
	// csResolver 合约规格查询（D-077 修复⑪A M1 平仓 PnL 权威化）。nil = CS 回退 1.0。
	csResolver ContractSizeResolver
	// pnlCalibrator 历史单权威 PnL 回读（D-077 修复⑪A M1 回读校准）。nil = 用同步公式。
	pnlCalibrator PnLCalibrator
	// fxResolver 计价币→USD 换算（D-105 T2 平仓 PnL 计价币换算）。nil = 非 USD 计价 fx 不可得。
	fxResolver FxResolver
	// revalidateAbsBps 复核劣化绝对阈值 bps（D-077 修复⑪B N4，ARB_REVALIDATE_ABS_BPS 默认 2.0）。
	revalidateAbsBps float64
	// revalidateRelPct 复核劣化相对阈值 %（D-077 修复⑪B N4，ARB_REVALIDATE_REL_PCT 默认 20）。
	revalidateRelPct float64
	// mtDisasterSlPct MT 灾难 SL 距离百分比（D-146 S1，ARB_MT_DISASTER_SL_PCT 默认 2.0）。
	// ≤0 = 禁用（不挂 SL）。仅 MT 腿（:mt4/:mt5）下单时用，CEX 腿忽略。
	mtDisasterSlPct float64

	// D-159 W1a R1 S2.3：SWAPARB 精确组合门禁——真实名义额检查。
	// swapArbComboAllow 非 nil 且 posType=SWAPARB 时，建 position 前校验组合允许+名义额+gross。
	// nil = 未接线（gate 层 + dashboard 手动入口兜底，pipeline 不重复检查）。
	swapArbComboAllow []ComboAllowSpec
	notionalUSD       float64

	// D-311 S6：XASSET_CARRY 精确组合门禁——零信任显式白名单。
	// posType=XASSET_CARRY 时无条件校验（空表=全拒），canonical_sell 从 opportunities 行解析。
	xassetComboAllow []XAssetComboAllowSpec

	// D-189 P0-2 → D-193-A 扩包（决策 8）：现金流写入 seam——生产默认调 p.store.InsertCashflowFull，
	// 测试注入失败函数验证显式错误处理。签名改 Full 形态（接收 *pgstore.Cashflow，返回 (won, err)）。
	cashflowWriter func(ctx context.Context, c *pgstore.Cashflow) (won bool, err error)

	// D-193-A 扩包 S5：结算 seam——closePositionCore 关闭后触发 Settle（修复 479 缺 settlement 行根因）。
	// nil=未接线（reconcile.autoClose 路径仍由 reconcile 侧 settler 触发；手动 ClosePosition/ResumeClose 路径需此 seam）。
	// 失败不回滚 CLOSED（settlement 可重试，settlement.go SettleAt 幂等）。
	settler Settler

	// D-189 P0-4：per-position close 锁——同进程并发 ClosePosition/ResumeClose 只允许一个执行。
	closeMu  sync.Mutex
	closeSem map[int64]*sync.Mutex // per-posID mutex（closeMu 保护此 map）

	// D-195-B：执行环境 demo/live（从 config 注入，写 positions.environment）。
	environment string
}

// Settler D-193-A 扩包 S5：结算接口（closePositionCore 关闭后触发，修复 479 缺 settlement 行）。
// main 装配时注入 settlement.Settle 包装；nil=未接线（兼容旧测试）。
type Settler interface {
	Settle(ctx context.Context, posID int64) error
}

// SetSettler D-193-A 扩包 S5：注入结算器（main 装配时调）。
func (p *Pipeline) SetSettler(s Settler) { p.settler = s }

// NewPipeline 创建执行管线。
func NewPipeline(reg *Registry, store *pgstore.Store, qbus *bus.QuoteBus, limits RiskLimits, environment string) *Pipeline {
	return &Pipeline{
		registry:         reg,
		store:            store,
		qbus:             qbus,
		limits:           limits,
		environment:      environment,
		revalidateAbsBps: 2.0, // D-077 修复⑪B N4 默认值（SetRevalidateThresholds 可覆盖）
		revalidateRelPct: 20.0,
		closeSem:         make(map[int64]*sync.Mutex), // D-189 P0-4
	}
}

// Execute 方法见 pipeline_execute.go（从 pipeline.go 拆出保 ≤300 行）。
