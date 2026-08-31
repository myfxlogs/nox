// Package config 集中管理全链路经济参数（D-035：禁止散落硬编码魔法数字）。
// 全部 env 可覆盖（ARB_* 前缀），默认值单一出处；阈值初值取保守默认，
// 由 D-034 统计复盘以真实成交数据校准。
package config

import (
	"time"
)

// Config 全链路经济参数。
type Config struct {
	// === 检测器阈值（§5，息差型统一为年化百分点，D-036）===
	CrossVenueMinGrossBpsCEX    float64       // CEX(spot/perp) 跨所 gross 门槛（bps）。D-035：cross 降级，仅报价错位捕获
	CrossVenueMinGrossBpsCFD    float64       // CFD(mt4/mt5) 跨所 gross 观察门槛（bps）。低于保本 2.6，展示仍靠评估器 net>0（D-051 WP1）
	FundingMinNetAnnualized     float64       // funding hedge 净年化门槛（%）
	FundingBaselinePct          float64       // 同所 funding 基准费率年化（%，ARB_FUNDING_BASELINE_PCT，缺省 10.95=0.01%/8h×3×365；D-084 基准≠机会，同所扣后过门槛）
	CarryMinNetAnnualized       float64       // carry 净年化门槛（%）——D-036 由 bps 改百分点，与 funding 对齐
	PerpFundingMinNetAnnualized float64       // perp×perp funding 差净年化门槛（%）——D-064
	GrabMinNetAnnualized        float64       // grab 档门槛（%），替代 classify 硬编码 4.5（D-036）
	DetectorFreshnessTTL        time.Duration // 报价陈旧排除 TTL

	// === 评估器成本（§6）===
	SlippageBps      float64       // 单腿滑点（bps）
	MarginRateAnnual float64       // 资金机会成本年化（%）——业主按实际资金状况设，D-036
	OpportunityTTL   time.Duration // 机会过期 TTL
	ScanThrottle     time.Duration // 引擎扫描节流（替代硬编码 30s，D-036）

	// === 持仓监控（§13.4）===
	CloseBpsThr         float64       // 建议平仓：预期净收益 ≤ 该值（bps）
	AlertResendInterval time.Duration // 同持仓同类告警冷却（ARB_ALERT_RESEND_INTERVAL，默认 10m，D-072 铃铛治理）

	// === 佣金（bps/腿）——per-venue 真实定价（D-036）===
	CommissionCFDBps      float64            // MT4/MT5 单腿回退默认（账号 commission_bps NULL 时用）
	CommissionCEXTakerBps float64            // CEX taker 单腿回退默认
	CommissionTaker       map[string]float64 // CEX 逐交易所 taker（binance/okx/gate）

	// === MT swap 采集（§3.6）===
	SwapRefresh time.Duration // swap 轮询间隔（默认 1h，swap 变化慢）
	// TradeModeTTL trade_mode Fact 新鲜度（D-060 F4/F5 共用）：超期视为未知放行
	//（broker 周末置 Disabled 不跨周末误杀周一；venue 服务端拒单是最终权威）。
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
	// SwapArbEquityUseRatio swap 套利名义额资金使用率系数（ARB_SWAPARB_EQUITY_USE_RATIO，缺省 0.5）。
	// notional = min(equityLong, equityShort) × ratio；保留抗单余地（单腿裸露承受力）。
	// ratio=1 等价旧行为（100% 资金使用率）；不限额=无绝对上限，ratio=单组合资金使用率。
	SwapArbEquityUseRatio float64
	// SwapArbBareCap swap 套利单腿裸露上限系数（ARB_SWAPARB_BARE_CAP，缺省 1.0）。
	// D-097 R2：notional = minEq×ratio，若 > minEq×BareCap 则 cap 到 minEq×BareCap（只 cap 不 skip）。
	// 语义=单腿名义≤BareCap 倍权益，防大权益+高 ratio 放大裸露。1.0=单腿名义≤1 倍权益。
	SwapArbBareCap float64
	// SwapArbReentryCooldownDays 组合平仓后重入冷却天数（ARB_SWAPARB_REENTRY_COOLDOWN_DAYS，缺省 7）。
	// 该 ComboKey 存在 CLOSED 持仓且 closed_at > now() − N 天 → 跳过（不尝试/不弹窗/不落 REJECTED 噪音）。
	// 冷却外或无 CLOSED 记录 → 放行；0 = 关闭冷却（始终放行）。
	SwapArbReentryCooldownDays int
	// SwapArbVenueExposureCap 单 venue SWAPARB 合计裸露上限系数（ARB_SWAPARB_VENUE_EXPOSURE_CAP，缺省 2.0）。
	// D-132 T1：新开仓时若任一腿 venue 现有 SWAPARB 活仓合计名义 + 新仓名义 > venue equity × cap → skip。
	// 语义=单 venue SWAPARB 裸露 ≤ cap 倍权益；注资后各档自动满足，组合数失控时硬顶。0 = 不启用。
	SwapArbVenueExposureCap float64
	// SwapArbMarginBudgetPct 组合保证金预算告警阈值（ARB_SWAPARB_MARGIN_BUDGET_PCT，缺省 0.8）——D-142 ④。
	// 双腿合计保证金 / min(equity) > 此值 → 观察版 warn 告警（只告警不拦截）。
	// 保证金 = notional × 1/leverage（账户级回落，品种级 InitMarginRate 网关不返回）。
	// 0 = 不启用。无限杠杆（2e9）→ 保证金≈0，不告警。
	SwapArbMarginBudgetPct float64
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

	// === 自动交易（D-070/D-072）===
	AutoTradeEnabled      bool          // 总开关（ARB_AUTOTRADE_ENABLED，缺省 false）
	AutoTradeMaxNotional  float64       // 单笔最大名义额 USD（缺省 1000）
	AutoTradeMaxPositions int           // 最大同时持仓数（缺省 5）
	AutoTradeMaxDailyLoss float64       // 单日最大亏损 USD（缺省 100）
	AutoTradeMinNetBps    float64       // 最低净收益门槛 bps（缺省 1.0，D-070 均值回归降低门槛）
	AutoTradePollInterval time.Duration // OPEN 持仓扫描间隔（缺省 10s）

	// D-072 §3.4 硬编码外部化（默认值=现行为，零迁移）
	AutoTradeAllowVenues       map[string]bool // ARB_AUTOTRADE_ALLOW_VENUES（默认 277842155:mt5,REDACTED_MT_ACCT:mt5）
	AutoTradeAllowCanonicals   map[string]bool // ARB_AUTOTRADE_ALLOW_CANONICALS（默认 NAS100,ETHUSDT,XAUUSD,BTCUSDT）
	AutoTradeExcludeCanonicals map[string]bool // ARB_AUTOTRADE_EXCLUDE_CANONICALS（默认 BRENT,WTI）
	// D-159 W1a R1：精确组合门禁（SWAPARB 专用）。
	// SwapArbAllowCombosRaw 原始字符串（分号分隔记录，每条 canonical|venueLong|venueShort|maxLongQty|maxShortQty|maxGrossExposureUSD）。
	// 由 autotrade.parseComboAllow 解析。空 = fail-closed（RUNNING 下无组合被允许）。
	SwapArbAllowCombosRaw  string
	SwapArbMaxActiveCombos int                // ARB_SWAPARB_MAX_ACTIVE_COMBOS，默认 1（D-138 单组合口径）
	MeanRevertThresholds   map[string]float64 // ARB_MEANREVERT_THRESHOLDS（默认 NAS100:1.5,ETHUSDT:0.5,XAUUSD:0.5）

	// D-072 §3.3/§4.2 swap/funding 退出参数
	AutoTradeMinNetSwapBps float64 // ARB_AUTOTRADE_MIN_NET_SWAP_BPS（默认 0.2）
	SwapArbExitNetBps      float64 // ARB_SWAPARB_EXIT_NET_BPS（默认 0）
	FundingExitNetPct      float64 // ARB_FUNDING_EXIT_NET_PCT（默认 0）

	// === 复核劣化双阈值（D-077 修复⑪B N4）===
	RevalidateAbsBps float64 // ARB_REVALIDATE_ABS_BPS（默认 2.0）
	RevalidateRelPct float64 // ARB_REVALIDATE_REL_PCT（默认 20）

	// === Swap 套利入场成本容忍（D-077 修复⑪B N6）===
	SwapArbEntrySpreadMinBps float64 // ARB_SWAPARB_ENTRY_SPREAD_MIN_BPS（默认 -3）
	// D-159 W4a R7：单腿 bid-ask spread 宽度上限（bps），>0 才校验，0=不校验。
	SwapArbMaxLegSpreadBps float64 // ARB_SWAPARB_MAX_LEG_SPREAD_BPS（默认 50.0，保守阈值：SWAPARB 跨账户 FX 品种 bid-ask spread 通常 <10 bps，50 bps 是 5 倍保守）

	// === 前端分析员（D-091）===
	// AIBaseURL/Key/Model/PGDSN 四 env + RatePerHour 限流；空 Key=未配置（RPC 返回 failed_precondition）。
	AIBaseURL          string // ARB_AI_BASE_URL（如 https://open.bigmodel.cn/api/paas/v4）
	AIAPIKey           string // ARB_AI_API_KEY（仅 .env，不进仓）
	AIModel            string // ARB_AI_MODEL（如 glm-4.6）
	AIPGDSN            string // ARB_AI_PG_DSN（arb_ro 只读角色 DSN）
	AnalystRatePerHour int    // ARB_ANALYST_RATE_PER_HOUR（默认 20）
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
}

// Load 从环境变量加载配置，未设置用默认值。
func Load() *Config {
	return &Config{
		CrossVenueMinGrossBpsCEX:    envFloat("ARB_CROSS_MIN_GROSS_BPS_CEX", 10.0),
		CrossVenueMinGrossBpsCFD:    envFloat("ARB_CROSS_MIN_GROSS_BPS_CFD", 20.0), // D-078 round-trip 实证：CFD round-trip ~7.5bps+基差~5bps → gross≥20 才可能盈利
		FundingMinNetAnnualized:     envFloat("ARB_FUNDING_MIN_NET_ANNUALIZED", 4.0),
		FundingBaselinePct:          envFloat("ARB_FUNDING_BASELINE_PCT", 10.95), // D-084 同所基准费率扣除
		CarryMinNetAnnualized:       envFloat("ARB_CARRY_MIN_NET_ANNUALIZED", 4.0),
		PerpFundingMinNetAnnualized: envFloat("ARB_PERP_FUNDING_MIN_NET_ANNUALIZED", 2.0),
		GrabMinNetAnnualized:        envFloat("ARB_GRAB_MIN_NET_ANNUALIZED", 4.5),
		DetectorFreshnessTTL:        envDur("ARB_DETECTOR_FRESHNESS_TTL", 120*time.Second), // D-070 均值回归：放宽 freshness 让低频报价 venue 配对
		SlippageBps:                 envFloat("ARB_SLIPPAGE_BPS", 0.3),                     // D-070 均值回归：降低滑点假设（CFD 限价单）
		MarginRateAnnual:            envFloat("ARB_MARGIN_RATE_ANNUAL", 4.5),
		OpportunityTTL:              envDur("ARB_OPPORTUNITY_TTL", 30*time.Second),
		ScanThrottle:                envDur("ARB_SCAN_THROTTLE", 30*time.Second),
		CloseBpsThr:                 envFloat("ARB_CLOSE_BPS_THR", 0.5),                  // D-070 均值回归：价差回归到 0.5bps 内平仓
		AlertResendInterval:         envDur("ARB_ALERT_RESEND_INTERVAL", 10*time.Minute), // D-072 铃铛治理：同 key 告警冷却
		CommissionCFDBps:            envFloat("ARB_COMMISSION_CFD_BPS", 0.3),
		CommissionCEXTakerBps:       envFloat("ARB_COMMISSION_CEX_TAKER_BPS", 5.0), // D-145：binance/okx VIP0 taker 实测 0.05%=5bps（账户级真值待真金 key 后 commissionFor 精确化）
		// D-159 W4a T6：CommissionTaker map 默认 5.0（与 CommissionCEXTakerBps 对齐，D-145 意图生效）。
		// W0 T6 铁证：旧默认 10.0 使 CommissionCEXTakerBps 5.0 永不回退，opportunities 表 79 条 CEX×CEX commission_cost=20.0（10×2 腿）。
		CommissionTaker: map[string]float64{
			"binance": envFloat("ARB_COMMISSION_TAKER_BINANCE", 5.0),
			"okx":     envFloat("ARB_COMMISSION_TAKER_OKX", 5.0),
			"gate":    envFloat("ARB_COMMISSION_TAKER_GATE", 5.0),
		},
		SwapRefresh:                envDur("ARB_SWAP_REFRESH", time.Hour),
		TradeModeTTL:               envDur("ARB_TRADEMODE_TTL", 24*time.Hour),
		MaxLegImbalancePct:         envFloat("ARB_MAX_LEG_IMBALANCE_PCT", 2.0),
		ReviewMinSamples:           envInt("ARB_REVIEW_MIN_SAMPLES", 3),
		ReviewCrossBiasBps:         envFloat("ARB_REVIEW_CROSS_BIAS_BPS", 1.0),
		ReviewCarryBiasAnnualPct:   envFloat("ARB_REVIEW_CARRY_BIAS_ANNUAL_PCT", 1.0),
		SymbolsTTL:                 envDur("ARB_SYMBOLS_TTL", 5*time.Minute),
		BinanceFuturesInfoURL:      envStr("ARB_BINANCE_FUTURES_INFO_URL", "https://testnet.binancefuture.com/fapi/v1/exchangeInfo"), // D-127 P2
		LiqMMR:                     envFloat("ARB_LIQ_MMR", 0.005),
		FundingDropRatio:           envFloat("ARB_FUNDING_DROP_RATIO", 0.5),
		SwapArbEquityUseRatio:      envFloat("ARB_SWAPARB_EQUITY_USE_RATIO", 0.5),
		SwapArbBareCap:             envFloat("ARB_SWAPARB_BARE_CAP", 1.0),
		SwapArbReentryCooldownDays: envInt("ARB_SWAPARB_REENTRY_COOLDOWN_DAYS", 7),
		SwapArbVenueExposureCap:    envFloat("ARB_SWAPARB_VENUE_EXPOSURE_CAP", 2.0),    // D-132 T1 venue 级敞口门
		SwapArbMarginBudgetPct:     envFloat("ARB_SWAPARB_MARGIN_BUDGET_PCT", 0.8),     // D-142 ④ 观察版告警
		PerpFundMinHold:            envDur("ARB_PERPFUNDING_MIN_HOLD", 8*time.Hour),    // D-133 T1 持有下限
		PerpFundExitDebounce:       envInt("ARB_PERPFUNDING_EXIT_DEBOUNCE", 3),         // D-133 T1 连续去抖
		FundingMaxBreakevenDays:    envFloat("ARB_FUNDING_MAX_BREAKEVEN_DAYS", 1.0),    // D-133 T2 回本周期上门
		StoreMinNetBps:             envFloat("ARB_STORE_MIN_NET_BPS", 5.0),             // D-078：CROSS_VENUE 入库门槛，round-trip 后 net≥5bps 才入库
		FundingHedgeMinNetStore:    envFloat("ARB_FUNDING_HEDGE_MIN_NET_STORE", 14.95), // D-085 息差家族净门槛（含基准 10.95+门槛 4）
		PerpFundingMinNetStore:     envFloat("ARB_PERP_FUNDING_MIN_NET_STORE", 2.0),    // D-085
		CarryMinNetStore:           envFloat("ARB_CARRY_MIN_NET_STORE", 4.0),           // D-085
		VerifyWindowDays:           envInt("ARB_VERIFY_WINDOW_DAYS", 30),               // D-086 验证样本新鲜窗口
		VerifyMinSamples:           envInt("ARB_VERIFY_MIN_SAMPLES", 2),                // D-086 V1 最低样本数
		VerifyMinRatio:             envFloat("ARB_VERIFY_MIN_RATIO", 0.7),              // D-086 V1 最低 R
		BrokerPosPollInterval:      envDur("ARB_BROKER_POS_POLL_MS", 5*time.Second),
		OpportunityNotionalUSD:     envFloat("ARB_OPP_NOTIONAL_USD", 1000),
		AlertPaused:                envBool("ARB_ALERT_PAUSED", false),
		AutoTradeEnabled:           envBool("ARB_AUTOTRADE_ENABLED", false),
		AutoTradeMaxNotional:       envFloat("ARB_AUTOTRADE_MAX_NOTIONAL", 1000),
		AutoTradeMaxPositions:      envInt("ARB_AUTOTRADE_MAX_POSITIONS", 5),
		AutoTradeMaxDailyLoss:      envFloat("ARB_AUTOTRADE_MAX_DAILY_LOSS", 100),
		AutoTradeMinNetBps:         envFloat("ARB_AUTOTRADE_MIN_NET_BPS", 1.0), // D-070 均值回归：降低门槛让 277+591 波动价差通过
		AutoTradePollInterval:      envDur("ARB_AUTOTRADE_POLL_INTERVAL", 10*time.Second),
		AutoTradeAllowVenues:       envStringSet("ARB_AUTOTRADE_ALLOW_VENUES", "277842155:mt5,REDACTED_MT_ACCT:mt5"),
		AutoTradeAllowCanonicals:   envStringSet("ARB_AUTOTRADE_ALLOW_CANONICALS", ""), // D-122：默认空=全放行（gate 层风控兜底）
		AutoTradeExcludeCanonicals: envStringSet("ARB_AUTOTRADE_EXCLUDE_CANONICALS", "BRENT,WTI"),
		SwapArbAllowCombosRaw:      envStr("ARB_SWAPARB_ALLOW_COMBOS", ""),     // D-159 W1a R1：空=fail-closed
		SwapArbMaxActiveCombos:     envInt("ARB_SWAPARB_MAX_ACTIVE_COMBOS", 1), // D-138 单组合口径
		MeanRevertThresholds:       envFloatMap("ARB_MEANREVERT_THRESHOLDS", "NAS100:1.5,ETHUSDT:0.5,XAUUSD:0.5"),
		AutoTradeMinNetSwapBps:     envFloat("ARB_AUTOTRADE_MIN_NET_SWAP_BPS", 0.2),
		SwapArbExitNetBps:          envFloat("ARB_SWAPARB_EXIT_NET_BPS", 0),
		FundingExitNetPct:          envFloat("ARB_FUNDING_EXIT_NET_PCT", 0),
		RevalidateAbsBps:           envFloat("ARB_REVALIDATE_ABS_BPS", 2.0),            // D-077 修复⑪B N4
		RevalidateRelPct:           envFloat("ARB_REVALIDATE_REL_PCT", 20.0),           // D-077 修复⑪B N4
		SwapArbEntrySpreadMinBps:   envFloat("ARB_SWAPARB_ENTRY_SPREAD_MIN_BPS", -3.0), // D-077 修复⑪B N6
		SwapArbMaxLegSpreadBps:     envFloat("ARB_SWAPARB_MAX_LEG_SPREAD_BPS", 50.0),   // D-159 W4a R7
		AIBaseURL:                  envStr("ARB_AI_BASE_URL", ""),
		AIAPIKey:                   envStr("ARB_AI_API_KEY", ""),
		AIModel:                    envStr("ARB_AI_MODEL", "glm-4.6"),
		AIPGDSN:                    envStr("ARB_AI_PG_DSN", ""),
		AnalystRatePerHour:         envInt("ARB_ANALYST_RATE_PER_HOUR", 20),
		MTData:                     loadMTData(),
		SwapReconcileDeviationPct:  envFloat("ARB_SWAP_RECONCILE_DEVIATION_PCT", 10.0), // D-110 P2
		MTDisasterSlPct:            envFloat("ARB_MT_DISASTER_SL_PCT", 2.0),            // D-146 S1 broker 端灾难 SL
		SwapNonSettleVenues:        envStringSlice("ARB_SWAP_NON_SETTLE_VENUES"),       // D-112
		FreezeGuard:                envDur("ARB_FREEZE_GUARD", 10*time.Minute),         // D-155 freeze guard 冻结腿预检窗口
		Environment:                envStr("ARB_ENVIRONMENT", "demo"),                  // D-159 W1a I6：缺省 demo fail-safe
	}
}
