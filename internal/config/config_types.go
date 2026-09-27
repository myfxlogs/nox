// config_types.go —— Config struct 定义（D-451 S2：自 config.go 整搬，纯搬家零语义）。
package config

import (
	"time"
)

// Config 全链路经济参数。
type Config struct {
	// === 检测器阈值（§5，息差型统一为年化百分点，D-036）===
	CrossVenueMinGrossBpsCEX    float64 // CEX(spot/perp) 跨所 gross 门槛（bps）。D-035：cross 降级，仅报价错位捕获
	CrossVenueMinGrossBpsCFD    float64 // CFD(mt4/mt5) 跨所 gross 观察门槛（bps）。低于保本 2.6，展示仍靠评估器 net>0（D-051 WP1）
	FundingMinNetAnnualized     float64 // funding hedge 净年化门槛（%）
	FundingBaselinePct          float64 // 同所 funding 基准费率年化（%，ARB_FUNDING_BASELINE_PCT，缺省 10.95=0.01%/8h×3×365；D-084 基准≠机会，同所扣后过门槛）
	CarryMinNetAnnualized       float64 // carry 净年化门槛（%）——D-036 由 bps 改百分点，与 funding 对齐
	PerpFundingMinNetAnnualized float64 // perp×perp funding 差净年化门槛（%）——D-064
	// === D-300 跨标的同质资产套利（XASSET_CARRY）—— 字段定义见 config_xasset.go（XassetConfig 嵌入，字段访问不变）===
	XassetConfig
	PfAdmitConfig                      // D-491 PERP_FUNDING 证据准入（config_pfadmission.go，同 D-479 嵌入先例）
	GrabMinNetAnnualized float64       // grab 档门槛（%），替代 classify 硬编码 4.5（D-036）
	DetectorFreshnessTTL time.Duration // 报价陈旧排除 TTL

	// === 评估器成本（§6）===
	SlippageBps                  float64       // 单腿滑点（bps）
	MarginRateAnnual             float64       // 资金机会成本年化（%）——业主按实际资金状况设，D-036
	OpportunityTTL, ScanThrottle time.Duration // 机会过期 TTL + 引擎扫描节流（替代硬编码 30s，D-036）

	// === 持仓监控（§13.4）===
	CloseBpsThr         float64       // 建议平仓：预期净收益 ≤ 该值（bps）
	AlertResendInterval time.Duration // 同持仓同类告警冷却（ARB_ALERT_RESEND_INTERVAL，默认 10m，D-072 铃铛治理）

	// === 佣金（bps/腿）——per-venue 真实定价（D-036）===
	CommissionCFDBps       float64            // MT4/MT5 单腿回退默认（账号 commission_bps NULL 时用）
	CommissionCEXTakerBps  float64            // CEX taker 单腿回退默认
	CommissionTaker        map[string]float64 // CEX 逐交易所 taker（binance/okx/gate）
	XAssetCexFeeBps        float64            // D-391：回本「本」CEX fee 估算率（缺省 5.0），fee_usd NULL 回退
	MarginReservePct       float64            // D-415：开仓资金预检可用额安全垫（缺省 0.20），venue 原生校验乘 (1−pct)
	GateDefaultLeverage    float64            // D-472 S2：gate venue 级缺省杠杆档（ARB_GATE_DEFAULT_LEVERAGE，缺省 5；账户级取不到的第二级）
	MinLiqDistancePct      float64            // D-472 S4：isolated 强平距离下限（ARB_MIN_LIQ_DISTANCE_PCT，缺省 0=关闸；非零前置=回放定标）
	CrossNotionalEquityCap float64            // D-472 S4：cross 名义/权益占比上限（ARB_CROSS_NOTIONAL_EQUITY_CAP，缺省 0.5；≤0=cross 支路关）

	// === MT swap 采集（§3.6）===
	SwapRefresh time.Duration // swap 轮询间隔（默认 1h，swap 变化慢）
	// TradeModeTTL trade_mode Fact 新鲜度（D-060 F4/F5 共用）：超期视为未知放行（broker 周末置 Disabled 不误杀周一；venue 拒单为最终权威）。
	TradeModeTTL       time.Duration
	MaxLegImbalancePct float64 // 卖腿名义不平衡上限（百分比，D-028/D-060 实施修正②）

	// === 复盘闭环（§13.6，D-034）===
	ReviewMinSamples         int     // 聚合出调参建议的最小样本数（防单笔噪声）
	ReviewCrossBiasBps       float64 // 价差型平均偏差超此值才发建议（bps）
	ReviewCarryBiasAnnualPct float64 // 息差型平均偏差超此值才发建议（年化百分点）

	// === Broker 符号表缓存（D-045 §3.2.1）===
	SymbolsTTL time.Duration // symbolGroups per-venue 缓存 TTL

	// === CEX step 元数据（D-127 P2）===
	BinanceFuturesInfoURL string // binance perp exchangeInfo 直连 URL（默认 testnet；真盘改 fapi.binance.com）

	// === 个人持仓实账（财务线工作项 3）===
	LiqMMR float64 // 理论强平维持保证金率（强平价 = entry × (1 + 1/leverage − MMR)；缺省 0.5%）
	// FundingDropRatio 资金费急降预警比例（今日 < 比例×前日 触发；缺省 0.5）——§11 收益侧方向证伪。
	FundingDropRatio float64

	// === Swap 套利风控（S6/B）===
	// SwapArbEquityUseRatio 名义额资金使用率系数（ARB_SWAPARB_EQUITY_USE_RATIO，缺省 0.5）：notional=min(equityLong,equityShort)×ratio；ratio=1=旧行为，不限额=无上限。
	SwapArbEquityUseRatio float64
	// SwapArbBareCap swap 套利单腿裸露上限系数（ARB_SWAPARB_BARE_CAP，缺省 1.0）。
	// D-097 R2：notional=minEq×ratio，>minEq×BareCap 则 cap 之（只 cap 不 skip）——单腿名义≤BareCap 倍权益。
	SwapArbBareCap float64
	// SwapArbReentryCooldownDays 组合平仓后重入冷却天数（ARB_SWAPARB_REENTRY_COOLDOWN_DAYS，缺省 7）。
	// 该 ComboKey 存在 CLOSED 持仓且 closed_at > now() − N 天 → 跳过（不尝试/不弹窗/不落 REJECTED 噪音）。
	// 冷却外或无 CLOSED 记录 → 放行；0 = 关闭冷却（始终放行）。
	SwapArbReentryCooldownDays int
	// StopReentryMinutes gate 止损重进冷却分钟数（ARB_STOP_REENTRY_MINUTES，缺省 60）。
	// D-372：同 ComboKey stop/manual 平仓后 N 分钟内 gate 拒绝自动重进（全策略统一，
	// 补机会驱动路径零覆盖）。60min=打碎 stop→秒级重进毒环且容 detector 分钟级重评；
	// 0 = 关闭检查（逃生阀，与 SwapArbReentryCooldownDays 语义对齐）。
	StopReentryMinutes int
	RotateMaxPerDay    int // D-406 轮转日限（ARB_ROTATE_MAX_PER_DAY，缺省 1；0=不启用）
	// XAssetExecDriftBps XASSET 执行质量闸阈（ARB_XASSET_EXEC_DRIFT_BPS，缺省 8.0）。
	// D-378a S2：|fill 口径−quote 锚|>thresh → exec_drift 平仓（双向都杀；entry_noise_bps 可抬阈）。
	XAssetExecDriftBps float64
	// PositionEvalSec 退出评估兜底扫描间隔秒（ARB_POSITION_EVAL_SEC，缺省 60）。
	// D-378a S3：评估饥荒修复——无订单事件时退出评估最多饿 10min → 60s（事件驱动路径不动）。
	PositionEvalSec int
	// D-378b 灾难带四件（撤窄阈基差止损，XASSET 限定；语义详 engine_disaster.go）：
	// DisasterBps=绝对下限（ARB_XASSET_DISASTER_BPS 缺省 40，≤0→40 防呆）；NoiseMult=noise
	// 倍数（…_NOISE_MULT 缺省 4，与绝对下限取 max，≤0→不乘）；Debounce=连续评估次数
	// （…_DEBOUNCE 缺省 2，单次尖刺不杀，≤1→单次即杀）；QuoteMaxAge=双腿报价新鲜度上限
	// （…_QUOTE_MAX_AGE 缺省 5m，超龄→fail_closed:stale 不判灾难，≤0→不查回退旧行为）。
	XAssetDisasterBps       float64
	XAssetDisasterNoiseMult float64
	XAssetDisasterDebounce  int
	XAssetQuoteMaxAge       time.Duration
	XAssetFeeHoldMaxDays    float64 // D-409 摊销门槛上限天（ARB_XASSET_FEE_HOLD_MAX_DAYS 缺省 2.0=48h）
	// ComboLossLimit 组合级止损保险丝（C1）：同 ComboKey stop/exec_drift 平仓计数 ≥ 此值 → gate 拒
	// （ARB_COMBO_LOSS_LIMIT，缺省 3）；0 = 关闭（逃生阀）。
	ComboLossLimit int
	// ComboLossWindowH 止损计数回看窗口小时（ARB_COMBO_LOSS_WINDOW_H，缺省 24）；0 = 关闭。
	ComboLossWindowH int
	// SwapArbVenueExposureCap 单 venue SWAPARB 合计裸露上限系数（ARB_SWAPARB_VENUE_EXPOSURE_CAP，缺省 2.0）。
	// D-132 T1：新开仓时若任一腿 venue 现有 SWAPARB 活仓合计名义 + 新仓名义 > venue equity × cap → skip。
	// 语义=单 venue SWAPARB 裸露 ≤ cap 倍权益；注资后各档自动满足，组合数失控时硬顶。0 = 不启用。
	SwapArbVenueExposureCap float64
	// SwapArbMarginBudgetPct 组合保证金预算告警阈值（ARB_SWAPARB_MARGIN_BUDGET_PCT，缺省 0.8）——D-142 ④。
	// 双腿合计保证金 / min(equity) > 此值 → 观察版 warn 告警（只告警不拦截）。
	// 保证金 = notional × 1/leverage（账户级回落，品种级 InitMarginRate 网关不返回）。
	// 0 = 不启用。无限杠杆（2e9）→ 保证金≈0，不告警。
	SwapArbMarginBudgetPct float64
	// D-181 S2：probe 探测仓——无实测腿的首仓自动成为 probe，到期评估升级/拒绝。
	// ProbeEnabled=false → 不标记不评估（向后兼容，默认 false）。
	ProbeEnabled bool
	// ProbeEvalDays probe 评估等待天数（ARB_PROBE_EVAL_DAYS，缺省 3 ≈ 2 结算日）。
	ProbeEvalDays int
	// PerpFundMinHold PERP_FUNDING 持有下限（ARB_PERPFUNDING_MIN_HOLD，缺省 8h）——D-133 T1。
	// 退出判定 floor：now − opened_at < MinHold → 不平仓（至少活过一个结算窗，收益源先参与）。
	// 0 = 不限（近似旧行为但仍有 debounce）。
	PerpFundMinHold time.Duration
	// PerpFundExitDebounce PERP_FUNDING 退出连续去抖次数（ARB_PERPFUNDING_EXIT_DEBOUNCE，缺省 3）——D-133 T1。
	// 净 funding ≤ 阈值需连续 N 次读数成立才平；任一次回正 → 计数清零。0 或 1 = 单次即平（近似旧行为）。
	PerpFundExitDebounce int
	// FundingMaxBreakevenDays 息差型回本周期上门（ARB_FUNDING_MAX_BREAKEVEN_DAYS，缺省 1.0）——D-133 T2。
	// breakevenDays > 此值 → NetBps 置负，使下游 funding_min% 过滤自然淘汰（不加新淘汰通道）。
	// 0 = 不启用（回本门关闭）。
	FundingMaxBreakevenDays float64

	// === 机会站内推送（D-050；修复⑭ S3 退役 minNotifyNetBps 发布门槛 → oppTopic 全量事件流）===
	// 发布门槛已退役，前端显示过滤常量 5 语义自立（useFrontConfig 默认值）。

	// === 机会入库门槛（D-078 round-trip 成本修正 / D-085 息差家族净门槛）===
	// StoreMinNetBps CROSS_VENUE 入库最低净收益（bps）；低于此值不入库（从源头消除垃圾机会）。
	// 仅适用于价差型（net 为一次性 bps）；息差型 net 为年化百分点，由检测器 MinNetAnnualized 门槛过滤。
	StoreMinNetBps float64
	// 息差家族入库净门槛（D-085：门槛必须作用在卡片显示的净=毛−全部摩擦；FUNDING_HEDGE 再减基准）。
	// 作用在 evaluator 扣成本后的 NetBps（年化百分点），检测器门槛保留为前置过滤（双保险无害）。
	// FundingHedgeMinNetStore 默认 14.95=4 门槛+10.95 基准（语义=扣成本后净−基准≥4；基准变化时随配）。
	FundingHedgeMinNetStore float64 // ARB_FUNDING_HEDGE_MIN_NET_STORE
	PerpFundingMinNetStore  float64 // ARB_PERP_FUNDING_MIN_NET_STORE（默认 2.0）
	CarryMinNetStore        float64 // ARB_CARRY_MIN_NET_STORE（默认 4.0）

	// === 机会验证阶梯（D-086 V-Ladder）===
	// VerifyWindowDays 验证样本新鲜窗口（ARB_VERIFY_WINDOW_DAYS，缺省 30 天）。
	// VerifyMinSamples V1 最低样本数（ARB_VERIFY_MIN_SAMPLES，缺省 2）。
	// VerifyMinRatio V1 最低 R=Σrealized/Σexpected（ARB_VERIFY_MIN_RATIO，缺省 0.7）。
	VerifyWindowDays int
	VerifyMinSamples int
	VerifyMinRatio   float64

	// === Broker 实持仓轮询（D-052）===
	// BrokerPosPollInterval Broker 实持仓视图轮询周期（ARB_BROKER_POS_POLL_MS，缺省 5s）。
	BrokerPosPollInterval time.Duration

	// === 机会净收益 USD（D-053）===
	// OpportunityNotionalUSD net_usd 换算的单笔名义金额（ARB_OPP_NOTIONAL_USD，缺省 1000）。
	// bps 无量纲 → USD 必须有单笔名义假设；标注「按单笔 $X 估算」。
	OpportunityNotionalUSD float64

	// === 告警暂停（D-069）===
	// AlertPaused 暂停全部告警写入（ARB_ALERT_PAUSED=true，缺省 false）。
	AlertPaused bool

	// === 自动交易（D-070/D-072）—— 字段定义见 config_autotrade.go（AutotradeConfig 嵌入，字段访问不变）===
	AutotradeConfig

	// === 复核劣化双阈值（D-077 修复⑪B N4）===
	RevalidateAbsBps float64 // ARB_REVALIDATE_ABS_BPS（默认 2.0）
	RevalidateRelPct float64 // ARB_REVALIDATE_REL_PCT（默认 20）

	// === Swap 套利入场成本容忍（D-077 修复⑪B N6）===
	SwapArbEntrySpreadMinBps float64 // ARB_SWAPARB_ENTRY_SPREAD_MIN_BPS（默认 -3）
	// D-159 W4a R7：单腿 bid-ask spread 宽度上限（bps），>0 才校验，0=不校验。
	SwapArbMaxLegSpreadBps float64 // ARB_SWAPARB_MAX_LEG_SPREAD_BPS（默认 50.0，保守阈值：SWAPARB 跨账户 FX 品种 bid-ask spread 通常 <10 bps，50 bps 是 5 倍保守）
	// D-169 P0：入场价差上限（bps），>0 才校验，0=不校验。
	// "太好的价差"=虚假价差（跨合约/不同标的），290 bps 的 BRENT 跨 broker 价差直接挡住。
	SwapArbEntrySpreadMaxBps float64 // ARB_SWAPARB_ENTRY_SPREAD_MAX_BPS（默认 30）
	// D-169 P0：入场基差上限（bps），>0 才校验，0=不校验。正负双向拦截。
	SwapArbEntryBasisMaxBps float64 // ARB_SWAPARB_ENTRY_BASIS_MAX_BPS（默认 30）
	// D-180 S3：基差止损阈值（bps），PnL_bps < -BasisStopBps → 平仓（默认 4）。
	SwapArbBasisStopBps float64 // ARB_SWAPARB_BASIS_STOP_BPS（默认 4）
	// D-328 S5：carry 入口 funding regime 持久性闸三参数。
	CarryRegimeWindowH    float64 // ARB_CARRY_REGIME_WINDOW_H（默认 24）
	CarryRegimeFloorPct   float64 // ARB_CARRY_REGIME_FLOOR_PCT（默认 2.0，年化百分点）
	CarryRegimeMinSamples int     // ARB_CARRY_REGIME_MIN_SAMPLES（默认 12）
	// D-180 S4：入场门禁——净息差厚度下限（bps/日，spec 源×折扣后比较；默认 1.0）。
	SwapArbMinNetCarryBps float64 // ARB_SWAPARB_MIN_NET_CARRY_BPS（默认 1.0）
	// D-180 S4：spec 源折扣系数（冷启动不裸信规格；默认 0.5）。仅作用入场门禁，止损/展示用原值。
	SwapArbSpecDiscount float64 // ARB_SWAPARB_SPEC_DISCOUNT（默认 0.5）
	// D-180 S4：回收期上限（天，2×spreadBps/净息差 ≤ 此值才放行；默认 30）。
	SwapArbMaxPaybackDays int // ARB_SWAPARB_MAX_PAYBACK_DAYS（默认 30）
	// D-180 S4：集中度上限（同 venue×symbol 活跃腿数 < 此值才放行；默认 2）。
	SwapArbMaxLegsPerSymbol int // ARB_SWAPARB_MAX_LEGS_PER_SYMBOL（默认 2）

	// === 前端分析员（D-091）===
	// AIBaseURL/Key/Model/PGDSN 四 env + RatePerHour 限流；空 Key=未配置（RPC 返回 failed_precondition）。
	AIBaseURL           string // ARB_AI_BASE_URL（如 https://open.bigmodel.cn/api/paas/v4）
	AIAPIKey            string // ARB_AI_API_KEY（仅 .env，不进仓）
	AIModel             string // ARB_AI_MODEL（如 glm-4.6）
	AIPGDSN             string // ARB_AI_PG_DSN（arb_ro 只读角色 DSN）
	RetroDisabled       bool   // ARB_RETRO_ENABLED=0/false 关复盘定时器（D-457）
	AnalystRatePerHour  int    // ARB_ANALYST_RATE_PER_HOUR（默认 20）
	ExplorerEnabled     bool   // ARB_EXPLORER_ENABLED 缺省 false——探矿员壳总闸（D-513：关=零行为变化）
	ExplorerRatePerHour int    // ARB_EXPLORER_RATE_PER_HOUR（默认 10，独立令牌桶）
	MacroEnabled        bool   // ARB_MACRO_ENABLED 缺省 false——宏观采集层总闸（D-528：关=零行为变化）
	// MTData MT 数据采集稳定性加固参数（D-104 P0）。
	MTData MTDataConfig

	// === Swap 对账告警（D-110 P2）===
	SwapReconcileDeviationPct float64 // ARB_SWAP_RECONCILE_DEVIATION_PCT（默认 10.0）

	// === MT 灾难止损（D-146 S1）===
	// MTDisasterSlPct broker 端灾难 SL 距离（名义百分比，ARB_MT_DISASTER_SL_PCT 默认 2.0）。
	// 下单即带 SL（原子，无开仓后补挂窗口）；多腿入场价下方、空腿上方。
	// ≤0 = 禁用回退现行为（不挂 SL）。与基差止损 A5 分层：A5 引擎主动平仓（连接正常），
	// 灾难 SL 是 broker 服务器端兜底（断连时才轮到它）——2% >> A5 常规触发距离。
	MTDisasterSlPct float64

	// === Swap 不结算 venue（D-112）===
	// SwapNonSettleVenues 已知不执行 swap 结算的 venue 列表（ARB_SWAP_NON_SETTLE_VENUES，逗号分隔）。
	// venue 格式含 venueType 后缀（如 "277842155:mt5"），与 PositionLeg.Venue 完整 key 一致。
	// swapledger 对这些 venue 记 0（不写 cashflow），避免按规格值误记。
	SwapNonSettleVenues []string

	// === D-256-A S4：组合级止损计数（reason-aware）===
	// SwapArbComboLossLimit 同 ComboKey 止损次数 ≥ 此值 → skip 开仓（ARB_SWAPARB_COMBO_LOSS_LIMIT，缺省 3）。
	// 0 = 关闭组合级止损拦截（向后兼容）。reason 过滤：仅计 basis loss / net swap，排除人工/probe。
	SwapArbComboLossLimit int
	// SwapArbComboLossWindowDays 止损计数回看窗口天数（ARB_SWAPARB_COMBO_LOSS_WINDOW_DAYS，缺省 30）。
	SwapArbComboLossWindowDays int

	// === D-256-A S5：复活扳机告警 ===
	// SwapArbRevivalBps netSwapBps >= 此值 且 swapSrc=settled → critical 告警（ARB_SWAPARB_REVIVAL_ALERT_BPS，缺省 2.2）。
	// 0 = 关闭复活告警。不自动复活，只告警提示业主/决策席复审。
	SwapArbRevivalBps float64

	// D-264：飞书告警推送凭据（ARB_FEISHU_APP_ID/APP_SECRET，空=禁用）。
	FeishuAppID     string
	FeishuAppSecret string
	// D-258 S2：TRX 能量租金率 floor 告警阈值（ARB_TRX_ENERGY_RATE_FLOOR_PCT，默认 8.0）。
	// 7d 实测年化 < floor → warn 告警（D-191 §9 触发复核）；0=禁用。
	TrxEnergyRateFloorPct float64
	// D-266 S5：逆回购利率雷达告警阈值+采集间隔。
	// RepoSpikeWarnPct GC007 > 此阈值 → warn（ARB_REPO_SPIKE_WARN_PCT，默认 3.0）；0=禁用。
	// RepoSpikeCriticalPct GC007 > 此阈值 → critical（ARB_REPO_SPIKE_CRITICAL_PCT，默认 5.0）；0=禁用。
	// RepoCollectInterval 采集间隔（ARB_REPO_COLLECT_INTERVAL，默认 5m）。
	RepoSpikeWarnPct     float64
	RepoSpikeCriticalPct float64
	RepoCollectInterval  time.Duration
	// D-270：周四操作提醒配置。
	// RepoIdleCny 闲置 CNY 金额（ARB_REPO_IDLE_CNY，默认 0=禁用；个人数据 .env gitignored D-047）。
	// RepoMinReturnCny 最小预期收益元（ARB_REPO_MIN_RETURN_CNY，默认 5）。
	// RepoReminderHour 提醒时段北京时间小时（ARB_REPO_REMINDER_HOUR，默认 10）。
	RepoIdleCny      float64
	RepoMinReturnCny float64
	RepoReminderHour int

	// === 交易时段防线（D-155 freeze guard）===
	// FreezeGuard 冻结腿预检窗口（ARB_FREEZE_GUARD，缺省 10m）。
	// 开仓前双腿各自校验「该腿报价在窗口内发生过价格值变化」；任一腿冻结 → 整组 abort（LEGGED 零容忍）。
	// ≤0 = 禁用（全放行）。与 D-104 freshness 分层：freshness=时间戳门禁（采集侧），freeze guard=价格变化门禁（执行侧）。
	FreezeGuard time.Duration

	// === D-159 W1a I6：environment/is_real 一致性检查 ===
	// Environment 执行环境固定值（ARB_ENVIRONMENT，'demo'/'live'，缺省 'demo' fail-safe）。
	// 开仓前校验 venue 的 broker_accounts.is_real 与此值一致：is_real=true ↔ environment='live'。
	// is_real=nil（未知）→ fail-closed 拒绝（不允许未知环境开仓）。
	Environment string
	// D-159 W6a S2：LIVE 人工开关（ARB_LIVE_TRADE，默认 false）。
	// is_real=true 账户 + LiveTrade=false → 拒绝开仓。demo 不受限制。
	LiveTrade bool
	// D-165 S1：swap 偏差拦截阈值（ARB_SWAP_DEVIATION_GATE_PCT，默认 50.0）。
	// 同 venue 已有 OPEN 持仓的 broker 实际 swap vs facts 规格值偏差超阈值→拒绝新开仓。
	SwapDeviationGatePct float64
	// D-165 S2：swap 偏差退出阈值（ARB_SWAP_DEVIATION_EXIT_PCT，默认 50.0）。
	// recalcNetSwap 对拍偏差超阈值→用 broker 实际值触发退出。
	SwapDeviationExitPct float64
	// D-165 S3：swap facts 零值校验开关（ARB_SWAP_FACTS_ZERO_CHECK，默认 true）。
	// 任一腿 swap facts=0 但 broker 该 venue 有非零 swap 历史→fail-closed 拒绝。
	SwapFactsZeroCheckEnabled bool
	// D-195-C1：capital snapshot 新鲜度 TTL（ARB_CAPITAL_SNAPSHOT_TTL，默认 5m）。
	// LiveGate 第 5 条 + readiness capital_snapshot_ready 层用。capital_accounts 表无 TTL 列，从 config 读取。
	CapitalSnapshotTTL time.Duration
	// === D-269 TRX 投票收益优化器 ===
	TrxVoteMinSRs          int     // 最少分散 SR 数（ARB_TRX_VOTE_MIN_SRS，默认 3）
	TrxVoteMaxSRs          int     // 最多分散 SR 数（ARB_TRX_VOTE_MAX_SRS，默认 3，D-355）
	TrxVoteMaxBrokerage    int     // 最大可接受 brokerage（ARB_TRX_VOTE_MAX_BROKERAGE，默认 20）
	TrxVoteYieldDropPct    float64 // 触发改投的收益下降阈值（ARB_TRX_VOTE_YIELD_DROP_PCT，默认 15.0）
	TrxVoteBetterOppPct    float64 // 触发改投的更优机会阈值（ARB_TRX_VOTE_BETTER_OPP_PCT，默认 20.0）
	TrxVoteMaxRebalPerDay  int     // 每日最大改投次数（ARB_TRX_VOTE_MAX_REBAL_PER_DAY，默认 1）
	TrxVoteMinIntervalMin  int     // D-293 S3：改投防抖间隔分钟（ARB_TRX_VOTE_MIN_INTERVAL_MINUTES，默认 120=2h）
	TrxVoteSpreadThreshold float64 // D-297：价差触发改投阈值（ARB_TRX_VOTE_SPREAD_THRESHOLD，默认 0.001）
	// === D-271 CEX 只读扫描器 ===
	CexScanInterval time.Duration // 扫描间隔（ARB_CEX_SCAN_INTERVAL，默认 5m）
	CexScanSymbols  []string      // 价差扫描币种（ARB_CEX_SCAN_SYMBOLS，默认 BTC/ETH/TRX/SOL/USDT/USDC）
	// D-405 urgent/warn 档告警冷却（默认 30m/6h，env ARB_MARGIN_URGENT/WARN_COOLDOWN）。
	MarginUrgentCooldown, MarginWarnCooldown time.Duration // 同上行注释

	// === D-274 事件驱动研判引擎 ===
	JudgeBalanceHKD float64 // sizing 双币余额 HKD（ARB_JUDGE_BALANCE_HKD；0=未配置 sizing 降级 qty=0）
	JudgeBalanceUSD float64 // sizing 双币余额 USD（ARB_JUDGE_BALANCE_USD；0=同上）
}
