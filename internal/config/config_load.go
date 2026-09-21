package config

// Load 从环境变量加载配置（D-261 S6 从 config.go 搬出，纯搬家）。

import (
	"os"

	"time"
)

// Load 从环境变量加载配置，未设置用默认值。
func Load() *Config {
	return &Config{
		CrossVenueMinGrossBpsCEX:    envFloat("ARB_CROSS_MIN_GROSS_BPS_CEX", 10.0),
		CrossVenueMinGrossBpsCFD:    envFloat("ARB_CROSS_MIN_GROSS_BPS_CFD", 20.0), // D-078 round-trip 实证：CFD round-trip ~7.5bps+基差~5bps → gross≥20 才可能盈利
		FundingMinNetAnnualized:     envFloat("ARB_FUNDING_MIN_NET_ANNUALIZED", 4.0),
		FundingBaselinePct:          envFloat("ARB_FUNDING_BASELINE_PCT", 10.95), // D-084 同所基准费率扣除
		CarryMinNetAnnualized:       envFloat("ARB_CARRY_MIN_NET_ANNUALIZED", 4.0),
		PerpFundingMinNetAnnualized: envFloat("ARB_PERP_FUNDING_MIN_NET_ANNUALIZED", 2.0),
		XassetEntryMinBps:           envFloat("ARB_XASSET_ENTRY_MIN_BPS", -20.0),    // D-300：容忍 ≤0.2% 逆价进场（evaluator 计入回本天数）
		XassetMinNetAnnualPct:       envFloat("ARB_XASSET_MIN_NET_ANNUAL_PCT", 0.0), // D-300：净息差>0 即候选（margin 扣减在 evaluator）
		XassetTrackingMaxBps:        envFloat("ARB_XASSET_TRACKING_MAX_BPS", 50.0),  // D-300 §5.3
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
		BinanceFuturesInfoURL:      envStr("ARB_BINANCE_FUTURES_INFO_URL", "https://192.229.85.35:8443/fapi/v1/exchangeInfo"), // D-127 P2；走 cex-proxy 避免 451
		LiqMMR:                     envFloat("ARB_LIQ_MMR", 0.005),
		FundingDropRatio:           envFloat("ARB_FUNDING_DROP_RATIO", 0.5),
		SwapArbEquityUseRatio:      envFloat("ARB_SWAPARB_EQUITY_USE_RATIO", 0.5),
		SwapArbBareCap:             envFloat("ARB_SWAPARB_BARE_CAP", 1.0),
		SwapArbReentryCooldownDays: envInt("ARB_SWAPARB_REENTRY_COOLDOWN_DAYS", 7),
		StopReentryMinutes:         envInt("ARB_STOP_REENTRY_MINUTES", 60),             // D-372 gate 止损重进冷却
		XAssetExecDriftBps:         envFloat("ARB_XASSET_EXEC_DRIFT_BPS", 8.0),         // D-378a S2 执行质量闸
		PositionEvalSec:            envInt("ARB_POSITION_EVAL_SEC", 60),                // D-378a S3 评估节拍
		ComboLossLimit:             envInt("ARB_COMBO_LOSS_LIMIT", 3),                  // D-378a S4 组合保险丝
		ComboLossWindowH:           envInt("ARB_COMBO_LOSS_WINDOW_H", 24),              // D-378a S4 保险丝窗口
		SwapArbVenueExposureCap:    envFloat("ARB_SWAPARB_VENUE_EXPOSURE_CAP", 2.0),    // D-132 T1 venue 级敞口门
		SwapArbMarginBudgetPct:     envFloat("ARB_SWAPARB_MARGIN_BUDGET_PCT", 0.8),     // D-142 ④ 观察版告警
		ProbeEnabled:               envBool("ARB_PROBE_ENABLED", false),                // D-181 S2 probe 探测仓总开关
		ProbeEvalDays:              envInt("ARB_PROBE_EVAL_DAYS", 3),                   // D-181 S2 probe 评估等待天数
		PerpFundMinHold:            envDur("ARB_PERPFUNDING_MIN_HOLD", 8*time.Hour),    // D-133 T1 持有下限
		PerpFundExitDebounce:       envInt("ARB_PERPFUNDING_EXIT_DEBOUNCE", 3),         // D-133 T1 连续去抖
		FundingMaxBreakevenDays:    envFloat("ARB_FUNDING_MAX_BREAKEVEN_DAYS", 1.0),    // D-133 T2 回本周期上门
		StoreMinNetBps:             envFloat("ARB_STORE_MIN_NET_BPS", 5.0),             // D-078：CROSS_VENUE 入库门槛，round-trip 后 net≥5bps 才入库
		FundingHedgeMinNetStore:    envFloat("ARB_FUNDING_HEDGE_MIN_NET_STORE", 14.95), // D-085 息差家族净门槛（含基准 10.95+门槛 4）
		PerpFundingMinNetStore:     envFloat("ARB_PERP_FUNDING_MIN_NET_STORE", 2.0),    // D-085
		CarryMinNetStore:           envFloat("ARB_CARRY_MIN_NET_STORE", 4.0),           // D-085
		XassetCarryMinNetStore:     envFloat("ARB_XASSET_CARRY_MIN_NET_STORE", 0.0),    // D-300 G5
		VerifyWindowDays:           envInt("ARB_VERIFY_WINDOW_DAYS", 30),               // D-086 验证样本新鲜窗口
		VerifyMinSamples:           envInt("ARB_VERIFY_MIN_SAMPLES", 2),                // D-086 V1 最低样本数
		VerifyMinRatio:             envFloat("ARB_VERIFY_MIN_RATIO", 0.7),              // D-086 V1 最低 R
		BrokerPosPollInterval:      envDur("ARB_BROKER_POS_POLL_MS", 5*time.Second),
		OpportunityNotionalUSD:     envFloat("ARB_OPP_NOTIONAL_USD", 1000),
		AlertPaused:                envBool("ARB_ALERT_PAUSED", false),
		AutotradeConfig:            *loadAutotradeConfig(),
		RevalidateAbsBps:           envFloat("ARB_REVALIDATE_ABS_BPS", 2.0),            // D-077 修复⑪B N4
		RevalidateRelPct:           envFloat("ARB_REVALIDATE_REL_PCT", 20.0),           // D-077 修复⑪B N4
		SwapArbEntrySpreadMinBps:   envFloat("ARB_SWAPARB_ENTRY_SPREAD_MIN_BPS", -3.0), // D-077 修复⑪B N6
		SwapArbMaxLegSpreadBps:     envFloat("ARB_SWAPARB_MAX_LEG_SPREAD_BPS", 50.0),   // D-159 W4a R7
		SwapArbEntrySpreadMaxBps:   envFloat("ARB_SWAPARB_ENTRY_SPREAD_MAX_BPS", 30.0), // D-169 P0
		SwapArbEntryBasisMaxBps:    envFloat("ARB_SWAPARB_ENTRY_BASIS_MAX_BPS", 30.0),  // D-169 P0
		SwapArbBasisStopBps:        envFloat("ARB_SWAPARB_BASIS_STOP_BPS", 4.0),        // D-180 S3
		CarryRegimeWindowH:         envFloat("ARB_CARRY_REGIME_WINDOW_H", 24.0),        // D-328 S5
		CarryRegimeFloorPct:        envFloat("ARB_CARRY_REGIME_FLOOR_PCT", 2.0),        // D-328 S5
		CarryRegimeMinSamples:      envInt("ARB_CARRY_REGIME_MIN_SAMPLES", 12),         // D-328 S5
		SwapArbMinNetCarryBps:      envFloat("ARB_SWAPARB_MIN_NET_CARRY_BPS", 1.0),     // D-180 S4
		SwapArbSpecDiscount:        envFloat("ARB_SWAPARB_SPEC_DISCOUNT", 0.5),         // D-180 S4
		SwapArbMaxPaybackDays:      envInt("ARB_SWAPARB_MAX_PAYBACK_DAYS", 30),         // D-180 S4
		SwapArbMaxLegsPerSymbol:    envInt("ARB_SWAPARB_MAX_LEGS_PER_SYMBOL", 2),       // D-180 S4
		AIBaseURL:                  envStr("ARB_AI_BASE_URL", ""),
		AIAPIKey:                   envStr("ARB_AI_API_KEY", ""),
		AIModel:                    envStr("ARB_AI_MODEL", "glm-4.6"),
		AIPGDSN:                    envStr("ARB_AI_PG_DSN", ""),
		AnalystRatePerHour:         envInt("ARB_ANALYST_RATE_PER_HOUR", 20),
		MTData:                     loadMTData(),
		SwapReconcileDeviationPct:  envFloat("ARB_SWAP_RECONCILE_DEVIATION_PCT", 10.0), // D-110 P2
		MTDisasterSlPct:            envFloat("ARB_MT_DISASTER_SL_PCT", 2.0),            // D-146 S1 broker 端灾难 SL
		SwapNonSettleVenues:        envStringSlice("ARB_SWAP_NON_SETTLE_VENUES"),       // D-112
		SwapArbComboLossLimit:      envInt("ARB_SWAPARB_COMBO_LOSS_LIMIT", 3),          // D-256-A S4
		SwapArbComboLossWindowDays: envInt("ARB_SWAPARB_COMBO_LOSS_WINDOW_DAYS", 30),   // D-256-A S4
		SwapArbRevivalBps:          envFloat("ARB_SWAPARB_REVIVAL_ALERT_BPS", 2.2),     // D-256-A S5
		TrxEnergyRateFloorPct:      envFloat("ARB_TRX_ENERGY_RATE_FLOOR_PCT", 8.0),     // D-258 S2
		RepoSpikeWarnPct:           envFloat("ARB_REPO_SPIKE_WARN_PCT", 3.0),           // D-266 S5
		RepoSpikeCriticalPct:       envFloat("ARB_REPO_SPIKE_CRITICAL_PCT", 5.0),       // D-266 S5
		RepoCollectInterval:        envDur("ARB_REPO_COLLECT_INTERVAL", 5*time.Minute), // D-266 S5
		RepoIdleCny:                envFloat("ARB_REPO_IDLE_CNY", 0),                   // D-270 S2（个人数据 .env gitignored）
		RepoMinReturnCny:           envFloat("ARB_REPO_MIN_RETURN_CNY", 5.0),           // D-270 S2
		RepoReminderHour:           envInt("ARB_REPO_REMINDER_HOUR", 10),               // D-270 S2
		FeishuAppID:                os.Getenv("ARB_FEISHU_APP_ID"),                     // D-264
		FeishuAppSecret:            os.Getenv("ARB_FEISHU_APP_SECRET"),                 // D-264
		FreezeGuard:                envDur("ARB_FREEZE_GUARD", 10*time.Minute),         // D-155 freeze guard 冻结腿预检窗口
		Environment:                envStr("ARB_ENVIRONMENT", "demo"),                  // D-159 W1a I6：缺省 demo fail-safe
		LiveTrade:                  envBool("ARB_LIVE_TRADE", false),                   // D-159 W6a S2：LIVE 人工开关默认 false
		SwapDeviationGatePct:       envFloat("ARB_SWAP_DEVIATION_GATE_PCT", 50.0),      // D-165 S1：swap 偏差拦截默认 50%
		SwapDeviationExitPct:       envFloat("ARB_SWAP_DEVIATION_EXIT_PCT", 50.0),      // D-165 S2：swap 偏差退出默认 50%
		SwapFactsZeroCheckEnabled:  envBool("ARB_SWAP_FACTS_ZERO_CHECK", true),         // D-165 S3：swap facts 零值校验默认开
		CapitalSnapshotTTL:         envDur("ARB_CAPITAL_SNAPSHOT_TTL", 5*time.Minute),  // D-195-C1：capital snapshot TTL 默认 5m
		// === D-269 TRX 投票收益优化器 ===
		TrxVoteMinSRs:          envInt("ARB_TRX_VOTE_MIN_SRS", 3),                // 最少分散 SR 数
		TrxVoteMaxSRs:          envInt("ARB_TRX_VOTE_MAX_SRS", 3),                // 最多分散 SR 数（D-355）
		TrxVoteMaxBrokerage:    envInt("ARB_TRX_VOTE_MAX_BROKERAGE", 20),         // 最大 brokerage 20%
		TrxVoteYieldDropPct:    envFloat("ARB_TRX_VOTE_YIELD_DROP_PCT", 15.0),    // 收益下降阈值 15%
		TrxVoteBetterOppPct:    envFloat("ARB_TRX_VOTE_BETTER_OPP_PCT", 0.0),     // D-285：更优机会阈值降零（任何更高就改投）
		TrxVoteMaxRebalPerDay:  envInt("ARB_TRX_VOTE_MAX_REBAL_PER_DAY", 1),      // 每日最大改投 1 次
		TrxVoteMinIntervalMin:  envInt("ARB_TRX_VOTE_MIN_INTERVAL_MINUTES", 120), // D-293 S3：防抖 2h
		TrxVoteSpreadThreshold: envFloat("ARB_TRX_VOTE_SPREAD_THRESHOLD", 0.001), // D-297：价差触发改投阈值 0.001pp
		// === D-271 CEX 只读扫描器 ===
		CexScanInterval: envDur("ARB_CEX_SCAN_INTERVAL", 5*time.Minute),
		CexScanSymbols:  cexScanSymbolsDefault(),
		// === D-274 事件驱动研判引擎 ===
		JudgeBalanceHKD: envFloat("ARB_JUDGE_BALANCE_HKD", 0),
		JudgeBalanceUSD: envFloat("ARB_JUDGE_BALANCE_USD", 0),
	}
}

// cexScanSymbolsDefault 读 ARB_CEX_SCAN_SYMBOLS，空则返回默认币种集（D-271）。
// D-325 S3：+XAU（XAUUSDT 金价永续进只读扫描集；cexSymbols 采集链 D-303 已含，
// funding_history 走 cexCfg 全符号无需另改）。
func cexScanSymbolsDefault() []string {
	if v := envStringSlice("ARB_CEX_SCAN_SYMBOLS"); v != nil {
		return v
	}
	return []string{"BTC", "ETH", "TRX", "SOL", "USDT", "USDC", "XAU"}
}
