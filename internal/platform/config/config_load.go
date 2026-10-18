package config

// Load 从环境变量加载配置（D-261 S6 从 config.go 搬出，纯搬家）。

import (
	"time"
)

// Load 从环境变量加载配置，未设置用默认值。
func Load() *Config {
	return &Config{
		CrossVenueMinGrossBpsCEX:    envFloat("NOX_CROSS_MIN_GROSS_BPS_CEX", 10.0),
		CrossVenueMinGrossBpsCFD:    envFloat("NOX_CROSS_MIN_GROSS_BPS_CFD", 20.0), // D-078 round-trip 实证：CFD round-trip ~7.5bps+基差~5bps → gross≥20 才可能盈利
		FundingMinNetAnnualized:     envFloat("NOX_FUNDING_MIN_NET_ANNUALIZED", 4.0),
		FundingBaselinePct:          envFloat("NOX_FUNDING_BASELINE_PCT", 10.95), // D-084 同所基准费率扣除
		CarryMinNetAnnualized:       envFloat("NOX_CARRY_MIN_NET_ANNUALIZED", 4.0),
		PerpFundingMinNetAnnualized: envFloat("NOX_PERP_FUNDING_MIN_NET_ANNUALIZED", 2.0),
		// 字段族折 config_xasset.go 嵌入——字面量改嵌套（promoted 字面量 go1.26 禁）。
		XassetConfig: XassetConfig{
			XassetEntryMinBps:      envFloat("NOX_XASSET_ENTRY_MIN_BPS", -20.0),    // D-300：容忍 ≤0.2% 逆价进场（evaluator 计入回本天数）
			XassetMinNetAnnualPct:  envFloat("NOX_XASSET_MIN_NET_ANNUAL_PCT", 0.0), // D-300：净息差>0 即候选（margin 扣减在 evaluator）
			XassetTrackingMaxBps:   envFloat("NOX_XASSET_TRACKING_MAX_BPS", 50.0),  // D-300 §5.3
			XassetCarryMinNetStore: envFloat("NOX_XASSET_CARRY_MIN_NET_STORE", 0.0),
		},
		GrabMinNetAnnualized:  envFloat("NOX_GRAB_MIN_NET_ANNUALIZED", 4.5),
		DetectorFreshnessTTL:  envDur("NOX_DETECTOR_FRESHNESS_TTL", 120*time.Second), // D-070 均值回归：放宽 freshness 让低频报价 venue 配对
		SlippageBps:           envFloat("NOX_SLIPPAGE_BPS", 0.3),                     // D-070 均值回归：降低滑点假设（CFD 限价单）
		MarginRateAnnual:      envFloat("NOX_MARGIN_RATE_ANNUAL", 4.5),
		OpportunityTTL:        envDur("NOX_OPPORTUNITY_TTL", 30*time.Second),
		OppPublishDeltaBps:    envFloat("NOX_OPP_PUBLISH_DELTA_BPS", 0.5),          // D-004 P1#8：发布去重实质变化阈值
		CloseBpsThr:           envFloat("NOX_CLOSE_BPS_THR", 0.5),                  // D-070 均值回归：价差回归到 0.5bps 内平仓
		AlertResendInterval:   envDur("NOX_ALERT_RESEND_INTERVAL", 10*time.Minute), // D-072 铃铛治理：同 key 告警冷却
		CommissionCFDBps:      envFloat("NOX_COMMISSION_CFD_BPS", 0.3),
		CommissionCEXTakerBps: envFloat("NOX_COMMISSION_CEX_TAKER_BPS", 5.0), // D-145：binance/okx VIP0 taker 实测 0.05%=5bps（账户级真值待真金 key 后 commissionFor 精确化）
		// CommissionTaker map 默认 5.0（与 CommissionCEXTakerBps 对齐，D-145 意图生效）。
		// W0 T6 铁证：旧默认 10.0 使 CommissionCEXTakerBps 5.0 永不回退，opportunities 表 79 条 CEX×CEX commission_cost=20.0（10×2 腿）。
		XAssetCexFeeBps:        envFloat("NOX_XASSET_CEX_FEE_BPS", 5.0),        // D-391：回本列「本」CEX fee NULL 回退估算率
		MarginReservePct:       envFloat("NOX_MARGIN_RESERVE_PCT", 0.20),       // D-415：开仓资金预检安全垫
		MinLiqDistancePct:      envFloat("NOX_MIN_LIQ_DISTANCE_PCT", 0),        // D-472 S4：缺省 0=关闸（定标前置）
		CrossNotionalEquityCap: envFloat("NOX_CROSS_NOTIONAL_EQUITY_CAP", 0.5), // D-472 S4：cross 占比上限
		PerpLeverageTarget:     envFloat("NOX_PERP_LEVERAGE_TARGET", 10),       // D-079：perp 腿目标杠杆（开仓前置 set-leverage）
		XAssetMTFirst:          envBool("NOX_XASSET_MT_FIRST", true),           // D-084：MT 腿先行顺序 taker（MT 拒单零成本）
		CommissionTaker: map[string]float64{
			"binance": envFloat("NOX_COMMISSION_TAKER_BINANCE", 5.0),
			"okx":     envFloat("NOX_COMMISSION_TAKER_OKX", 5.0),
			"gate":    envFloat("NOX_COMMISSION_TAKER_GATE", 5.0),
		},
		SwapRefresh:              envDur("NOX_SWAP_REFRESH", time.Hour),
		TradeModeTTL:             envDur("NOX_TRADEMODE_TTL", 24*time.Hour),
		MaxLegImbalancePct:       envFloat("NOX_MAX_LEG_IMBALANCE_PCT", 2.0),
		ReviewMinSamples:         envInt("NOX_REVIEW_MIN_SAMPLES", 3),
		ReviewCrossBiasBps:       envFloat("NOX_REVIEW_CROSS_BIAS_BPS", 1.0),
		ReviewCarryBiasAnnualPct: envFloat("NOX_REVIEW_CARRY_BIAS_ANNUAL_PCT", 1.0),
		SymbolsTTL:               envDur("NOX_SYMBOLS_TTL", 5*time.Minute),
		BinanceFuturesInfoURL:    envStr("NOX_BINANCE_FUTURES_INFO_URL", "https://fapi.binance.com/fapi/v1/exchangeInfo"),    // D-127 P2；默认官方端点，CN 部署经 env/DB 覆盖指向 cex-proxy 域名避 451
		BinanceSpotInfoURL:       envStr("NOX_BINANCE_SPOT_INFO_URL", "https://data-api.binance.vision/api/v3/exchangeInfo"), // D-551 S2：默认免墙公开域；可覆盖指 cex-proxy
		LiqMMR:                   envFloat("NOX_LIQ_MMR", 0.005),
		FundingDropRatio:         envFloat("NOX_FUNDING_DROP_RATIO", 0.5),
		// Swap 套利风控字段族折 config_swaprisk.go 嵌入——字面量改嵌套
		//（promoted 字面量禁，同 D-479 S1 XassetConfig 先例）。RotateMaxPerDay 同族并入。
		SwapArbRiskConfig: SwapArbRiskConfig{
			SwapArbEquityUseRatio:      envFloat("NOX_SWAPARB_EQUITY_USE_RATIO", 0.5),
			SwapArbBareCap:             envFloat("NOX_SWAPARB_BARE_CAP", 1.0),
			SwapArbReentryCooldownDays: envInt("NOX_SWAPARB_REENTRY_COOLDOWN_DAYS", 7),
			StopReentryMinutes:         envInt("NOX_STOP_REENTRY_MINUTES", 60),               // D-372 gate 止损重进冷却
			RotateMaxPerDay:            envInt("NOX_ROTATE_MAX_PER_DAY", 1),                  // D-406 轮转日限
			XAssetExecDriftBps:         envFloat("NOX_XASSET_EXEC_DRIFT_BPS", 8.0),           // D-378a S2 执行质量闸
			PositionEvalSec:            envInt("NOX_POSITION_EVAL_SEC", 60),                  // D-378a S3 评估节拍
			ComboLossLimit:             envInt("NOX_COMBO_LOSS_LIMIT", 3),                    // D-378a S4 组合保险丝
			ComboLossWindowH:           envInt("NOX_COMBO_LOSS_WINDOW_H", 24),                // D-378a S4 保险丝窗口
			XAssetDisasterBps:          envFloat("NOX_XASSET_DISASTER_BPS", 40.0),            // D-378b 灾难带绝对下限
			XAssetDisasterNoiseMult:    envFloat("NOX_XASSET_DISASTER_NOISE_MULT", 4.0),      // D-378b 灾难带 noise 倍数
			XAssetDisasterDebounce:     envInt("NOX_XASSET_DISASTER_DEBOUNCE", 2),            // D-378b 灾难带去抖
			XAssetQuoteMaxAge:          envDur("NOX_XASSET_QUOTE_MAX_AGE", 5*time.Minute),    // D-378b 报价新鲜度
			XAssetFastMoveBps:          envFloat("NOX_XASSET_FASTMOVE_BPS", 25.0),            // D-111 快行情位移阈
			XAssetFastMoveWindow:       envDur("NOX_XASSET_FASTMOVE_WINDOW", 90*time.Second), // D-111 位移窗
			SwapArbVenueExposureCap:    envFloat("NOX_SWAPARB_VENUE_EXPOSURE_CAP", 2.0),      // D-132 T1 venue 级敞口门
			SwapArbMarginBudgetPct:     envFloat("NOX_SWAPARB_MARGIN_BUDGET_PCT", 0.8),       // D-142 ④ 观察版告警
		},
		ProbeEnabled:               envBool("NOX_PROBE_ENABLED", false),                // D-181 S2 probe 探测仓总开关
		ProbeEvalDays:              envInt("NOX_PROBE_EVAL_DAYS", 3),                   // D-181 S2 probe 评估等待天数
		ProbeMaxNotionalUSD:        envFloat("NOX_PROBE_MAX_NOTIONAL", 1000),           // D-047：探针仓单腿名义上限
		ProbeRPCEnabled:            envBool("NOX_PROBE_RPC_ENABLED", false),            // D-047 R1：OpenProbe RPC 总开关（缺省关）
		ProbeMaxCostUSD:            envFloat("NOX_PROBE_MAX_COST", 100),                // D-163：机审 expected_cost 上限
		ProbeMaxOpen:               envInt("NOX_PROBE_MAX_OPEN", 2),                    // D-163：机审在途 OPEN PROBE 上限
		PerpFundMinHold:            envDur("NOX_PERPFUNDING_MIN_HOLD", 8*time.Hour),    // D-133 T1 持有下限
		PerpFundExitDebounce:       envInt("NOX_PERPFUNDING_EXIT_DEBOUNCE", 3),         // D-133 T1 连续去抖
		FundingMaxBreakevenDays:    envFloat("NOX_FUNDING_MAX_BREAKEVEN_DAYS", 1.0),    // D-133 T2 回本周期上门
		MaxBreakevenDays:           envFloat("NOX_MAX_BREAKEVEN_DAYS", 3.0),            // D-014 ① econ 入场回本上限
		HarvestHorizonDays:         envFloat("NOX_HARVEST_HORIZON_DAYS", 7.0),          // D-014 ② 收割比较地平
		RiskDD30USD:                envFloat("NOX_RISK_DD30_USD", -300),                // D-032/D-030：30d 账本净额停机线（含事故仓）
		RiskIncidentAckID:          envInt("NOX_RISK_INCIDENT_ACK_ID", 0),              // D-032：事故仓停新开 ack 游标（DB 行权威，env 兜底）
		RiskCexEquityPct:           envFloat("NOX_RISK_CEX_EQUITY_PCT", 35),            // D-032：CEX 权益纪律闸（%）
		RiskCfdEquityPct:           envFloat("NOX_RISK_CFD_EQUITY_PCT", 25),            // D-032：CFD/MT 权益纪律闸（%）
		CapitalBaseUSD:             envFloat("NOX_CAPITAL_BASE_USD", 10000),            // D-030：经营目标资金基数
		StoreMinNetBps:             envFloat("NOX_STORE_MIN_NET_BPS", 5.0),             // D-078：CROSS_VENUE 入库门槛，round-trip 后 net≥5bps 才入库
		FundingHedgeMinNetStore:    envFloat("NOX_FUNDING_HEDGE_MIN_NET_STORE", 14.95), // D-085 息差家族净门槛（含基准 10.95+门槛 4）
		PerpFundingMinNetStore:     envFloat("NOX_PERP_FUNDING_MIN_NET_STORE", 2.0),    // D-085
		CarryMinNetStore:           envFloat("NOX_CARRY_MIN_NET_STORE", 4.0),           // D-085
		VerifyWindowDays:           envInt("NOX_VERIFY_WINDOW_DAYS", 30),               // D-086 验证样本新鲜窗口
		VerifyMinSamples:           envInt("NOX_VERIFY_MIN_SAMPLES", 2),                // D-086 V1 最低样本数
		VerifyMinRatio:             envFloat("NOX_VERIFY_MIN_RATIO", 0.7),              // D-086 V1 最低 R
		BrokerPosPollInterval:      envDur("NOX_BROKER_POS_POLL_MS", 5*time.Second),
		OpportunityNotionalUSD:     envFloat("NOX_OPP_NOTIONAL_USD", 1000),
		AlertPaused:                envBool("NOX_ALERT_PAUSED", false),
		AutotradeConfig:            *loadAutotradeConfig(),
		RevalidateAbsBps:           envFloat("NOX_REVALIDATE_ABS_BPS", 2.0),            // D-077 修复⑪B N4
		RevalidateRelPct:           envFloat("NOX_REVALIDATE_REL_PCT", 20.0),           // D-077 修复⑪B N4
		SwapArbEntrySpreadMinBps:   envFloat("NOX_SWAPARB_ENTRY_SPREAD_MIN_BPS", -3.0), // D-077 修复⑪B N6
		SwapArbMaxLegSpreadBps:     envFloat("NOX_SWAPARB_MAX_LEG_SPREAD_BPS", 50.0),   // D-159 W4a R7
		SwapArbEntrySpreadMaxBps:   envFloat("NOX_SWAPARB_ENTRY_SPREAD_MAX_BPS", 30.0), // D-169 P0
		SwapArbEntryBasisMaxBps:    envFloat("NOX_SWAPARB_ENTRY_BASIS_MAX_BPS", 30.0),  // D-169 P0
		SwapArbBasisStopBps:        envFloat("NOX_SWAPARB_BASIS_STOP_BPS", 4.0),        // D-180 S3
		CarryRegimeWindowH:         envFloat("NOX_CARRY_REGIME_WINDOW_H", 24.0),        // D-328 S5
		CarryRegimeFloorPct:        envFloat("NOX_CARRY_REGIME_FLOOR_PCT", 2.0),        // D-328 S5
		CarryRegimeMinSamples:      envInt("NOX_CARRY_REGIME_MIN_SAMPLES", 12),         // D-328 S5
		SwapArbMinNetCarryBps:      envFloat("NOX_SWAPARB_MIN_NET_CARRY_BPS", 1.0),     // D-180 S4
		SwapArbSpecDiscount:        envFloat("NOX_SWAPARB_SPEC_DISCOUNT", 0.5),         // D-180 S4
		SwapArbMaxPaybackDays:      envInt("NOX_SWAPARB_MAX_PAYBACK_DAYS", 30),         // D-180 S4
		SwapArbMaxLegsPerSymbol:    envInt("NOX_SWAPARB_MAX_LEGS_PER_SYMBOL", 2),       // D-180 S4
		AIBaseURL:                  envStr("NOX_AI_BASE_URL", ""),
		AIAPIKey:                   envStr("NOX_AI_API_KEY", ""),
		AIModel:                    envStr("NOX_AI_MODEL", ""), // D-591 S7：硬编码型号兜底退役——DB/env 双空=LLM 段 fail-closed 禁用
		AIPGDSN:                    envStr("NOX_AI_PG_DSN", ""),
		AlertDedupWindow:           envDur("NOX_ALERT_DEDUP_WINDOW", 30*time.Minute), // D-592 S1：告警同键去重窗
		MakerFirstLegTimeout:       envDur("NOX_MAKER_TIMEOUT", 0),                   // D-018 阶段a：缺=未配置 fail-closed maker 停用
		MtQuoteStaleMin:            envInt("NOX_MT_QUOTE_STALE_MIN", 0),              // D-029 ②：缺=未配置 fail-closed 判闭市
		MakerMaxAttemptsPerHour:    envInt("NOX_MAKER_MAX_PER_HOUR", 0),              // D-018 阶段a：同上
		RetroDisabled:              !envBool("NOX_RETRO_ENABLED", true),              // D-457：env=0 关复盘定时器
		AnalystRatePerHour:         envInt("NOX_ANALYST_RATE_PER_HOUR", 20),
		ExplorerEnabled:            envBool("NOX_EXPLORER_ENABLED", false), // D-513：壳上线默认关
		ExplorerRatePerHour:        envInt("NOX_EXPLORER_RATE_PER_HOUR", 10),
		MacroEnabled:               envBool("NOX_MACRO_ENABLED", false),            // D-528：宏观采集默认关
		MacroBlackoutMin:           envInt("NOX_MACRO_BLACKOUT_MIN", 15),           // D-053 ③：缺省 ±15min
		MacroBlackoutCurrencies:    envStr("NOX_MACRO_BLACKOUT_CURRENCIES", "USD"), // D-053 ③：缺省 USD
		Stage1MinRowHours:          envInt("NOX_STAGE1_MIN_ROW_HOURS", 48),         // D-061 ①：缺省 48h
		Stage1OpenWarmupH:          envInt("NOX_STAGE1_OPEN_WARMUP_H", 2),          // D-061 ②：缺省 2h
		MTData:                     loadMTData(),
		SwapReconcileDeviationPct:  envFloat("NOX_SWAP_RECONCILE_DEVIATION_PCT", 10.0), // D-110 P2
		MTDisasterSlPct:            envFloat("NOX_MT_DISASTER_SL_PCT", 2.0),            // D-146 S1 broker 端灾难 SL
		SwapNonSettleVenues:        envStringSlice("NOX_SWAP_NON_SETTLE_VENUES"),       // D-112
		SwapArbComboLossLimit:      envInt("NOX_SWAPARB_COMBO_LOSS_LIMIT", 3),          // D-256-A S4
		SwapArbComboLossWindowDays: envInt("NOX_SWAPARB_COMBO_LOSS_WINDOW_DAYS", 30),   // D-256-A S4
		SwapArbRevivalBps:          envFloat("NOX_SWAPARB_REVIVAL_ALERT_BPS", 2.2),     // D-256-A S5
		TrxEnergyRateFloorPct:      envFloat("NOX_TRX_ENERGY_RATE_FLOOR_PCT", 8.0),     // D-258 S2
		FeishuAppID:                envStr("NOX_FEISHU_APP_ID", ""),                    // D-264（secrets overlay 注入通道同 AI_*）
		FeishuAppSecret:            envStr("NOX_FEISHU_APP_SECRET", ""),                // D-264（同上）
		FreezeGuard:                envDur("NOX_FREEZE_GUARD", 10*time.Minute),         // D-155 freeze guard 冻结腿预检窗口
		Environment:                envStr("NOX_ENVIRONMENT", "demo"),                  // D-159 W1a I6：缺省 demo fail-safe
		SwapDeviationGatePct:       envFloat("NOX_SWAP_DEVIATION_GATE_PCT", 50.0),      // D-165 S1：swap 偏差拦截默认 50%
		SwapDeviationExitPct:       envFloat("NOX_SWAP_DEVIATION_EXIT_PCT", 50.0),      // D-165 S2：swap 偏差退出默认 50%
		SwapFactsZeroCheckEnabled:  envBool("NOX_SWAP_FACTS_ZERO_CHECK", true),         // D-165 S3：swap facts 零值校验默认开
		// === D-269 TRX 投票收益优化器 ===
		TrxVoteMinSRs:          envInt("NOX_TRX_VOTE_MIN_SRS", 3),                // 最少分散 SR 数
		TrxVoteMaxSRs:          envInt("NOX_TRX_VOTE_MAX_SRS", 3),                // 最多分散 SR 数（D-355）
		TrxVoteMaxBrokerage:    envInt("NOX_TRX_VOTE_MAX_BROKERAGE", 20),         // 最大 brokerage 20%
		TrxVoteYieldDropPct:    envFloat("NOX_TRX_VOTE_YIELD_DROP_PCT", 15.0),    // 收益下降阈值 15%
		TrxVoteBetterOppPct:    envFloat("NOX_TRX_VOTE_BETTER_OPP_PCT", 0.0),     // D-285：更优机会阈值降零（任何更高就改投）
		TrxVoteMaxRebalPerDay:  envInt("NOX_TRX_VOTE_MAX_REBAL_PER_DAY", 1),      // 每日最大改投 1 次
		TrxVoteMinIntervalMin:  envInt("NOX_TRX_VOTE_MIN_INTERVAL_MINUTES", 120), // D-293 S3：防抖 2h
		TrxVoteSpreadThreshold: envFloat("NOX_TRX_VOTE_SPREAD_THRESHOLD", 0.001), // D-297：价差触发改投阈值 0.001pp
		// === D-271 CEX 只读扫描器 ===
		CexScanInterval: envDur("NOX_CEX_SCAN_INTERVAL", 5*time.Minute),
		CexScanSymbols:  cexScanSymbolsDefault(),
		// === D-405 账户资金三档监控 ===
		MarginUrgentCooldown: envDur("NOX_MARGIN_URGENT_COOLDOWN", 30*time.Minute),
		MarginWarnCooldown:   envDur("NOX_MARGIN_WARN_COOLDOWN", 6*time.Hour),
		// === D-274 事件驱动研判引擎 ===
		JudgeBalanceHKD: envFloat("NOX_JUDGE_BALANCE_HKD", 0),
		JudgeBalanceUSD: envFloat("NOX_JUDGE_BALANCE_USD", 0),
		// === D-145 env 收口 PG：原 env 直读键字段族（config_envknobs.go）===
		EnvKnobs: loadEnvKnobs(),
	}
}

// cexScanSymbolsDefault 读 NOX_CEX_SCAN_SYMBOLS，空则返回默认币种集（D-271）。
// +XAU（XAUUSDT 金价永续进只读扫描集；cexSymbols 采集链 D-303 已含，
// funding_history 走 cexCfg 全符号无需另改）。
func cexScanSymbolsDefault() []string {
	if v := envStringSlice("NOX_CEX_SCAN_SYMBOLS"); v != nil {
		return v
	}
	return []string{"BTC", "ETH", "TRX", "SOL", "USDT", "USDC", "XAU"}
}
