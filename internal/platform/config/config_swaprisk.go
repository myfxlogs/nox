// config_swaprisk.go —— Swap 套利风控字段族（自 config_types.go 整搬，纯搬家零语义——
// 保 ≤300 行门禁；Config 嵌入本类型，字段访问不变，同 XassetConfig 先例）。
package config

import "time"

// SwapArbRiskConfig Swap 套利风控（S6/B）字段族：额度/冷却/轮转/灾难带/保险丝/预算阈。
type SwapArbRiskConfig struct {
	// === Swap 套利风控（S6/B）===
	// SwapArbEquityUseRatio 名义额资金使用率系数（NOX_SWAPARB_EQUITY_USE_RATIO，缺省 0.5）：notional=min(equityLong,equityShort)×ratio；ratio=1=旧行为，不限额=无上限。
	SwapArbEquityUseRatio float64
	// SwapArbBareCap swap 套利单腿裸露上限系数（NOX_SWAPARB_BARE_CAP，缺省 1.0）。
	// notional=minEq×ratio，超 BareCap×minEq 则 cap 之（只 cap 不 skip），单腿名义≤BareCap 倍权益。
	SwapArbBareCap float64
	// SwapArbReentryCooldownDays 组合平仓后重入冷却天数（NOX_SWAPARB_REENTRY_COOLDOWN_DAYS，缺省 7）。
	// 该 ComboKey 存在 CLOSED 且 closed_at > now()−N 天 → 跳过（不弹窗/不落 REJECTED 噪音）；冷却外或无记录放行；0 = 关闭。
	SwapArbReentryCooldownDays int
	// StopReentryMinutes gate 止损重进冷却分钟数（NOX_STOP_REENTRY_MINUTES，缺省 60）。
	// 同 ComboKey stop/manual 平仓后 N 分钟内 gate 拒绝自动重进（全策略统一，
	// 补机会驱动路径零覆盖）。60min=打碎 stop→秒级重进毒环且容 detector 分钟级重评；
	// 0 = 关闭检查（逃生阀，与 SwapArbReentryCooldownDays 语义对齐）。
	StopReentryMinutes int
	RotateMaxPerDay    int // D-406 轮转日限（NOX_ROTATE_MAX_PER_DAY，缺省 1；0=不启用）
	// XAssetExecDriftBps XASSET 执行质量闸阈（NOX_XASSET_EXEC_DRIFT_BPS，缺省 8.0）。
	// |fill 口径−quote 锚|>thresh → exec_drift 平仓（双向都杀；entry_noise_bps 可抬阈）。
	XAssetExecDriftBps float64
	// PositionEvalSec 退出评估兜底扫描间隔秒（NOX_POSITION_EVAL_SEC，缺省 60）。
	// 评估饥荒修复——无订单事件时退出评估最多饿 10min → 60s（事件驱动路径不动）。
	PositionEvalSec int
	// 灾难带四件（撤窄阈基差止损，XASSET 限定；语义详 engine_disaster.go）：
	// DisasterBps=绝对下限（NOX_XASSET_DISASTER_BPS 缺省 40，≤0→40 防呆）；NoiseMult=noise
	// 倍数（…_NOISE_MULT 缺省 4，与绝对下限取 max，≤0→不乘）；Debounce=连续评估次数
	// （…_DEBOUNCE 缺省 2，单次尖刺不杀，≤1→单次即杀）；QuoteMaxAge=双腿报价新鲜度上限
	// （…_QUOTE_MAX_AGE 缺省 5m，超龄→fail_closed:stale 不判灾难，≤0→不查回退旧行为）。
	XAssetDisasterBps       float64
	XAssetDisasterNoiseMult float64
	XAssetDisasterDebounce  int
	XAssetQuoteMaxAge       time.Duration
	// 快行情挂起闸——腿 mid 窗口位移超阈 → 灾难判定本轮挂起（broker 大行情作弊窗内
	// CFD 价不可信；…_BPS 缺省 25、…_WINDOW 缺省 90s；任一 ≤0=闸关逃生阀）。
	XAssetFastMoveBps    float64
	XAssetFastMoveWindow time.Duration
	// ComboLossLimit 组合级止损保险丝（C1）：同 ComboKey stop/exec_drift 平仓计数 ≥ 此值 → gate 拒
	// （NOX_COMBO_LOSS_LIMIT，缺省 3）；0 = 关闭（逃生阀）。
	ComboLossLimit int
	// ComboLossWindowH 止损计数回看窗口小时（NOX_COMBO_LOSS_WINDOW_H，缺省 24）；0 = 关闭。
	ComboLossWindowH int
	// SwapArbVenueExposureCap 单 venue SWAPARB 合计裸露上限系数（NOX_SWAPARB_VENUE_EXPOSURE_CAP，缺省 2.0）。
	// 新开仓时任一腿 venue 现有 SWAPARB 活仓名义+新仓名义 > venue equity×cap → skip
	// （单 venue 裸露 ≤ cap 倍权益；注资后各档自动满足，组合数失控时硬顶）。0 = 不启用。
	SwapArbVenueExposureCap float64
	// SwapArbMarginBudgetPct 组合保证金预算告警阈值（NOX_SWAPARB_MARGIN_BUDGET_PCT，缺省 0.8）——D-142 ④。
	// 双腿合计保证金 / min(equity) > 此值 → 观察版 warn 告警（只告警不拦截）。
	// 保证金 = notional × 1/leverage（账户级回落，品种级 InitMarginRate 网关不返回）。
	// 0 = 不启用。无限杠杆（2e9）→ 保证金≈0，不告警。
	SwapArbMarginBudgetPct float64
}
