package autotrade

// Config 自动交易引擎配置（D-261 S2 从 engine.go 搬出，纯声明搬家）。

import (
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
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

	// D-311 S6：XASSET_CARRY 精确组合门禁——零信任显式白名单。
	// handleOpportunity 前置拦截：不命中→静默跳过（不产 intent 不落 REJECTED，
	// 修 462 条/时 REJECTED 刷屏）。空表=全拒。
	XAssetAllowCombos []exec.XAssetComboAllowSpec

	// D-328 S5：carry 入口 funding regime 持久性闸三参数（handleOpportunity 闸消费）。
	CarryRegime exec.CarryRegimeConfig

	// D-159 W1a I6：environment/is_real 一致性检查。
	// Environment 执行环境固定值（'demo'/'live'）。开仓前校验 venue 的 is_real 与此值一致。
	// 空 = 未配置（不检查，回退旧行为）。
	Environment string
	// D-159 W6a S2：LIVE 人工开关（ARB_LIVE_TRADE，默认 false）。
	// is_real=true 账户 + LiveTrade=false → 拒绝开仓（Gate.Submit + 手动 RPC 同源）。
	// is_real=false（demo）不受限制。切换=改 env+重启+审计日志（无 RPC）。
	LiveTrade bool
	// D-195-C1：LiveGate 检查器（LIVE 模式导出门）。nil=LIVE 直接拒绝（fail-closed）。
	LiveGateChecker *LiveGateChecker
	// D-165 S1：swap 偏差拦截阈值（默认 50.0）。同 venue 已有 OPEN 持仓偏差超阈值→拒绝新开仓。
	SwapDeviationGatePct float64
	// D-165 S2：swap 偏差退出阈值（默认 50.0）。recalcNetSwap 对拍偏差超阈值→用 broker 实际值。
	SwapDeviationExitPct float64
	// D-165 S3：swap facts 零值校验开关（默认 true）。facts=0 但 broker 有非零 swap 历史→拒绝。
	SwapFactsZeroCheckEnabled bool
	// D-165.1 S1：leg contract size resolver（MT 腿 CS≠1 时 notional 换算用）。
	// nil = 未注入（回退 CS=1，CEX 腿正确，MT 腿 notional 少算但不阻塞）。
	// 原生 CS 口径——只配原生 PriceActual（偏差/仓位链）。
	LegContractSizeResolver func(venueKey, venueSymbol string) (float64, bool)
	// D-594：canonical 单位数/手 resolver（CS_raw×qty_scale）——只配 canonical 报价
	// （swaparb rate 的 qbus mid）。nil = 回退 LegContractSizeResolver（非敏感品种等价）。
	LegUnitQtyResolver func(venueKey, venueSymbol string) (float64, bool)
	// D-190：QuoteBus 注入（偏差 gate usdnotional.PerLot JPY 换算用）。
	// nil = 未注入（JPY 腿 PerLot fail-closed 跳过；USD 腿不受影响）。
	Quotes *bus.QuoteBus

	// D-072 §3.3/§4.2 swap/funding 退出参数
	MinNetSwapBps         float64 // discoverSwapCombos 最低净 swap（ARB_AUTOTRADE_MIN_NET_SWAP_BPS，默认 0.2）
	SwapArbExitBps        float64 // SWAPARB/CARRY 退出净 swap bps（ARB_SWAPARB_EXIT_NET_BPS，默认 0）
	FundingExitPct        float64 // FUNDING_HEDGE/PERP_FUNDING 退出净年化（ARB_FUNDING_EXIT_NET_PCT，默认 0）
	CarryMinNetAnnualized float64 // D-187：netSwap 年化 ≥ 此值 → strategy="CARRY"（ARB_CARRY_MIN_NET_ANNUALIZED，默认 4.0）
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

	// D-372：gate 止损重进冷却——同 ComboKey stop/manual 平仓后窗口内拒绝自动重进
	// （gate 统一闸单点覆盖全 intent 来源；<=0 = 检查关闭，逃生阀）。
	StopReentryCooldown time.Duration

	// D-378a：XASSET 执行质量闸（exec_drift）+ 评估节拍 + 组合级止损保险丝（C1）。
	XAssetExecDriftBps       float64
	PositionFallbackInterval time.Duration // ≤0 → 回退 10min 旧值（engine.fallbackInterval 防呆）
	ComboLossLimit           int           // ≤0 = 检查关闭（逃生阀）
	ComboLossWindow          time.Duration

	// D-378b：灾难带（撤窄阈基差止损，XASSET 限定；SWAPARB/CARRY 的 basisStopCheck 不动）。
	XAssetDisasterBps       float64
	XAssetDisasterNoiseMult float64
	XAssetDisasterDebounce  int
	XAssetQuoteMaxAge       time.Duration // ≤0 = 不查新鲜度（回退旧行为）
	// D-409 收割费用摊销门槛：XAssetFeeHoldMaxDays=estDays 上限+延迟时钟上限（天，
	// ≤0→缺省 2.0 防呆）；XAssetCexFeeBps=fee_usd NULL 腿与平仓成本估算率（与 dashboard
	// enrich 同源同值，禁第二费率口径）。
	XAssetFeeHoldMaxDays float64
	XAssetCexFeeBps      float64

	// D-181 S2：probe 探测仓——无实测腿的首仓自动成为 probe，到期评估升级/拒绝。
	// ProbeEnabled=false → 不标记不评估（向后兼容，默认 false）。
	ProbeEnabled bool
	// ProbeEvalDays probe 评估等待天数（calendar days，默认 3 ≈ 2 结算日）。
	ProbeEvalDays int

	// D-406：轮转比较器日限（ARB_ROTATE_MAX_PER_DAY，默认 1；<=0=不启用日限——逃生阀）。
	RotateMaxPerDay int

	// D-142 ④：组合保证金预算观察版告警（只告警不拦截）。
	// 双腿合计保证金 / min(equity) > 此值 → warn 告警。0 = 不启用。
	// 杠杆不对称（两腿 leverage 差异显著）→ info 提示。
	SwapArbMarginBudgetPct float64

	// D-256-A S1：non-settle venue 列表（格式 "277842155:mt5"，含 venueType 后缀）。
	// swapRateFor 入口 guard：bare venue ∈ 集合 → 真值 0（实测不结算，非降级）。
	SwapNonSettleVenues []string

	// D-256-A S4：组合级止损计数（reason-aware，替代无差别 cooldown）。
	// SwapArbComboLossLimit=0 → 关闭组合级止损拦截（向后兼容）。
	SwapArbComboLossLimit int
	// SwapArbComboLossWindowDays 止损计数回看窗口天数（默认 30）。
	SwapArbComboLossWindowDays int

	// D-256-A S5：复活扳机告警阈值（settled-only，默认 2.2 bps/日 ≈ 年化 8.03%）。
	// netSwapBps >= 此值 且 swapSrc=settled → 发 critical 告警提示复活评估。
	// 0 = 关闭复活告警。不自动复活，只告警。
	SwapArbRevivalBps float64
}
