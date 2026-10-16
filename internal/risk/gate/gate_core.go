// gate_core.go —— 存量超标拆分（棘轮消化）：Gate 结构体+NewGate 构造从
// gate.go 纯搬移，零行为变。
package gate

import (
	"sync"
	"sync/atomic"
	"time"

	"nox/internal/portfolio/capitalview"
	"nox/internal/store/pgstore"
)

// Gate 统一开仓门禁（D-072 §3.1）。三 goroutine 串行化。
// 防重改 DB 实时派生——每次 Submit 实时查 ListActivePositions 派生组合键（治手动旁路）。
type Gate struct {
	mu    sync.Mutex
	store *pgstore.Store
	cfg   Config
	// 执行控制面（execution_controls 是执行模式唯一真相源）。
	controlPlane *ControlPlane
	// disabled 拒绝 stdout 1h 去重（复用 D-144 ② per-key 1h 模式）。
	// DB 落库保留全量（审计完整性不动），只压 stdout。内存，重启重置。
	lastDisabledLog   map[string]time.Time
	lastDisabledLogMu sync.Mutex
	// 开户门槛档检查器（dashboard.MarginFloorCheck 注入；nil=未接线不检查）。
	marginFloor marginFloorChecker
	// 执行面统一资金读口（与 dashboard 共享一实例；nil=未接线跳过 Src 闸——
	// dashboard 侧 marginFloorEval 仍保有 CEX 执行面 fail-closed 兜底）。
	capView *capitalview.View
	// 账号别名读口（main 装配 ListAccountAliases 闭包；nil=回落 bare 键）。
	venueAlias func(vk string) string
	// 资金拒单业主告警节流（per combo key ≥6h 至多一条 warn；内存态重启重置，
	// autotrade_log 全量审计不受节流影响）。
	lastMarginWarn   map[string]time.Time
	lastMarginWarnMu sync.Mutex
	// 拒单签名熔断（key=dedupKey|class；内存态重启重置可接受）。
	breaker   map[string]*rejectEntry
	breakerMu sync.Mutex
	swapArb   *atomic.Pointer[SwapArbComboSnapshot] // D-515 S5：表热读快照共享指针（nil=回退 cfg）
	// 预算一致性闸（D-262 M4.3④ 预留）：nil=未装配不消费——M4.4 挂实现后同点生效。
	budgetCheck BudgetChecker
}

// NewGate 创建门禁。
// 新增 controlPlane 参数（autotrade.New 内部自建，签名扩展第 3 参数）。
func NewGate(store *pgstore.Store, cfg Config, controlPlane *ControlPlane) *Gate {
	return &Gate{
		store:           store,
		cfg:             cfg,
		controlPlane:    controlPlane,
		lastDisabledLog: make(map[string]time.Time),
		lastMarginWarn:  make(map[string]time.Time),
	}
}
