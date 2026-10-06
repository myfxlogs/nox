// app_config_keys.go —— D-515 S4：app_config 白名单全量化（compose 实拍标量全集）。
// 逐键 typed setter（accessor 一行式）。排除：密钥类（app_secrets）、swaparb 组合（表化）、
// ENVIRONMENT/AUTH_TOKEN/BINANCE_PROXY_*（§4 留 env）、DEPRECATED 残键。
package config

import (
	"strconv"
	"strings"
	"time"
)

var appConfigKeys = map[string]AppCfgMeta{
	"ARB_ALERT_PAUSED":                    boolv(func(c *Config) *bool { return &c.AlertPaused }),
	"ARB_ALERT_RESEND_INTERVAL":           durv(func(c *Config) *time.Duration { return &c.AlertResendInterval }),
	"ARB_ANALYST_RATE_PER_HOUR":           intv(func(c *Config) *int { return &c.AnalystRatePerHour }),
	"ARB_AUTOTRADE_ENABLED":               boolv(func(c *Config) *bool { return &c.AutoTradeEnabled }),
	"ARB_AUTOTRADE_EXCLUDE_CANONICALS":    setv(func(c *Config) *map[string]bool { return &c.AutoTradeExcludeCanonicals }),
	"ARB_AUTOTRADE_MAX_DAILY_LOSS":        f64(func(c *Config) *float64 { return &c.AutoTradeMaxDailyLoss }),
	"ARB_AUTOTRADE_MAX_NOTIONAL":          f64(func(c *Config) *float64 { return &c.AutoTradeMaxNotional }),
	"ARB_AUTOTRADE_MAX_POSITIONS":         intv(func(c *Config) *int { return &c.AutoTradeMaxPositions }),
	"ARB_AUTOTRADE_MIN_NET_BPS":           f64(func(c *Config) *float64 { return &c.AutoTradeMinNetBps }),
	"ARB_AUTOTRADE_MIN_NET_SWAP_BPS":      f64(func(c *Config) *float64 { return &c.AutoTradeMinNetSwapBps }),
	"ARB_AUTOTRADE_POLL_INTERVAL":         durv(func(c *Config) *time.Duration { return &c.AutoTradePollInterval }),
	"ARB_BINANCE_FUTURES_INFO_URL":        strv(func(c *Config) *string { return &c.BinanceFuturesInfoURL }),
	"ARB_BINANCE_SPOT_INFO_URL":           strv(func(c *Config) *string { return &c.BinanceSpotInfoURL }),
	"ARB_BROKER_POS_POLL_MS":              durv(func(c *Config) *time.Duration { return &c.BrokerPosPollInterval }),
	"ARB_CAPITAL_SNAPSHOT_TTL":            durv(func(c *Config) *time.Duration { return &c.CapitalSnapshotTTL }),
	"ARB_CARRY_MIN_NET_ANNUALIZED":        f64(func(c *Config) *float64 { return &c.CarryMinNetAnnualized }),
	"ARB_CARRY_MIN_NET_STORE":             f64(func(c *Config) *float64 { return &c.CarryMinNetStore }),
	"ARB_CARRY_REGIME_FLOOR_PCT":          f64(func(c *Config) *float64 { return &c.CarryRegimeFloorPct }),
	"ARB_CARRY_REGIME_MIN_SAMPLES":        intv(func(c *Config) *int { return &c.CarryRegimeMinSamples }),
	"ARB_CARRY_REGIME_WINDOW_H":           f64(func(c *Config) *float64 { return &c.CarryRegimeWindowH }),
	"ARB_CEX_SCAN_INTERVAL":               durv(func(c *Config) *time.Duration { return &c.CexScanInterval }),
	"ARB_CEX_SCAN_SYMBOLS":                listv(func(c *Config) *[]string { return &c.CexScanSymbols }),
	"ARB_CLOSE_BPS_THR":                   f64(func(c *Config) *float64 { return &c.CloseBpsThr }),
	"ARB_COMBO_LOSS_LIMIT":                intv(func(c *Config) *int { return &c.ComboLossLimit }),
	"ARB_COMBO_LOSS_WINDOW_H":             intv(func(c *Config) *int { return &c.ComboLossWindowH }),
	"ARB_COMMISSION_CEX_TAKER_BPS":        f64(func(c *Config) *float64 { return &c.CommissionCEXTakerBps }),
	"ARB_COMMISSION_CFD_BPS":              f64(func(c *Config) *float64 { return &c.CommissionCFDBps }),
	"ARB_COMMISSION_TAKER_BINANCE":        takerG("binance"),
	"ARB_COMMISSION_TAKER_GATE":           takerG("gate"),
	"ARB_COMMISSION_TAKER_OKX":            takerG("okx"),
	"ARB_CROSS_MIN_GROSS_BPS_CEX":         f64(func(c *Config) *float64 { return &c.CrossVenueMinGrossBpsCEX }),
	"ARB_CROSS_MIN_GROSS_BPS_CFD":         f64(func(c *Config) *float64 { return &c.CrossVenueMinGrossBpsCFD }),
	"ARB_CROSS_NOTIONAL_EQUITY_CAP":       f64(func(c *Config) *float64 { return &c.CrossNotionalEquityCap }),
	"ARB_DETECTOR_FRESHNESS_TTL":          durv(func(c *Config) *time.Duration { return &c.DetectorFreshnessTTL }),
	"ARB_EXPLORER_ENABLED":                boolv(func(c *Config) *bool { return &c.ExplorerEnabled }),
	"ARB_MAKER_MAX_PER_HOUR":              intv(func(c *Config) *int { return &c.MakerMaxAttemptsPerHour }),        // D-018：每 opp 每小时 maker 挂单 attempt 上限（缺=停用）
	"ARB_MT_QUOTE_STALE_MIN":              intv(func(c *Config) *int { return &c.MtQuoteStaleMin }),                // D-029：MT 报价新鲜度阈值分钟（缺=fail-closed 判闭市）
	"ARB_MAKER_TIMEOUT":                   durv(func(c *Config) *time.Duration { return &c.MakerFirstLegTimeout }), // D-018：首腿 post-only 超时 T（缺=停用）
	"ARB_MACRO_ENABLED":                   boolv(func(c *Config) *bool { return &c.MacroEnabled }),
	"ARB_MACRO_BLACKOUT_MIN":              intv(func(c *Config) *int { return &c.MacroBlackoutMin }),           // D-053 ③：high 事件禁区窗 ±分钟
	"ARB_MACRO_BLACKOUT_CURRENCIES":       strv(func(c *Config) *string { return &c.MacroBlackoutCurrencies }), // D-053 ③：禁区币种集
	"ARB_STAGE1_MIN_ROW_HOURS":            intv(func(c *Config) *int { return &c.Stage1MinRowHours }),          // D-061 ①：行存活下限小时
	"ARB_STAGE1_OPEN_WARMUP_H":            intv(func(c *Config) *int { return &c.Stage1OpenWarmupH }),          // D-061 ②：周开盘暖场小时
	"ARB_CALIB_ENABLED":                   boolv(func(c *Config) *bool { return &c.CalibEnabled }),
	"ARB_EXPLORER_RATE_PER_HOUR":          intv(func(c *Config) *int { return &c.ExplorerRatePerHour }),
	"ARB_FREEZE_GUARD":                    durv(func(c *Config) *time.Duration { return &c.FreezeGuard }),
	"ARB_FUNDING_BASELINE_PCT":            f64(func(c *Config) *float64 { return &c.FundingBaselinePct }),
	"ARB_FUNDING_DROP_RATIO":              f64(func(c *Config) *float64 { return &c.FundingDropRatio }),
	"ARB_FUNDING_EXIT_NET_PCT":            f64(func(c *Config) *float64 { return &c.FundingExitNetPct }),
	"ARB_FUNDING_HEDGE_MIN_NET_STORE":     f64(func(c *Config) *float64 { return &c.FundingHedgeMinNetStore }),
	"ARB_FUNDING_MAX_BREAKEVEN_DAYS":      f64(func(c *Config) *float64 { return &c.FundingMaxBreakevenDays }),
	"ARB_MAX_BREAKEVEN_DAYS":              f64(func(c *Config) *float64 { return &c.MaxBreakevenDays }),
	"ARB_HARVEST_HORIZON_DAYS":            f64(func(c *Config) *float64 { return &c.HarvestHorizonDays }),
	"ARB_FUNDING_MIN_NET_ANNUALIZED":      f64(func(c *Config) *float64 { return &c.FundingMinNetAnnualized }),
	"ARB_GRAB_MIN_NET_ANNUALIZED":         f64(func(c *Config) *float64 { return &c.GrabMinNetAnnualized }),
	"ARB_JUDGE_BALANCE_HKD":               f64(func(c *Config) *float64 { return &c.JudgeBalanceHKD }),
	"ARB_JUDGE_BALANCE_USD":               f64(func(c *Config) *float64 { return &c.JudgeBalanceUSD }),
	"ARB_LIQ_MMR":                         f64(func(c *Config) *float64 { return &c.LiqMMR }),
	"ARB_LIVE_TRADE":                      boolv(func(c *Config) *bool { return &c.LiveTrade }),
	"ARB_MARGIN_FACT_MAX_AGE":             durv(func(c *Config) *time.Duration { return &c.MTData.MarginFactMaxAge }),
	"ARB_MARGIN_RATE_ANNUAL":              f64(func(c *Config) *float64 { return &c.MarginRateAnnual }),
	"ARB_MARGIN_RESERVE_PCT":              f64(func(c *Config) *float64 { return &c.MarginReservePct }),
	"ARB_MARGIN_URGENT_COOLDOWN":          durv(func(c *Config) *time.Duration { return &c.MarginUrgentCooldown }),
	"ARB_MARGIN_WARN_COOLDOWN":            durv(func(c *Config) *time.Duration { return &c.MarginWarnCooldown }),
	"ARB_MAX_LEG_IMBALANCE_PCT":           f64(func(c *Config) *float64 { return &c.MaxLegImbalancePct }),
	"ARB_MEANREVERT_THRESHOLDS":           fmapv(func(c *Config) *map[string]float64 { return &c.MeanRevertThresholds }),
	"ARB_MIN_LIQ_DISTANCE_PCT":            f64(func(c *Config) *float64 { return &c.MinLiqDistancePct }),
	"ARB_MT5_GATEWAY":                     strv(func(c *Config) *string { return &c.MTData.MT5Gateway }),
	"ARB_MT5_GATEWAY_FALLBACKS":           listv(func(c *Config) *[]string { return &c.MTData.MT5GatewayFallbacks }),
	"ARB_MT_DISASTER_SL_PCT":              f64(func(c *Config) *float64 { return &c.MTDisasterSlPct }),
	"ARB_MT_HEALTH_FACT_CRIT":             durv(func(c *Config) *time.Duration { return &c.MTData.HealthFactCrit }),
	"ARB_MT_HEALTH_MARGIN_WARN":           durv(func(c *Config) *time.Duration { return &c.MTData.HealthMarginWarn }), // D-067：margin 准静态独立阈
	"ARB_MT_HEALTH_MARGIN_CRIT":           durv(func(c *Config) *time.Duration { return &c.MTData.HealthMarginCrit }), // D-067
	"ARB_MT_HEALTH_FACT_WARN":             durv(func(c *Config) *time.Duration { return &c.MTData.HealthFactWarn }),
	"ARB_MT_HEALTH_QUOTE_CRIT":            durv(func(c *Config) *time.Duration { return &c.MTData.HealthQuoteCrit }),
	"ARB_MT_HEALTH_QUOTE_WARN":            durv(func(c *Config) *time.Duration { return &c.MTData.HealthQuoteWarn }),
	"ARB_MT_HEALTH_TICK":                  durv(func(c *Config) *time.Duration { return &c.MTData.HealthTick }),
	"ARB_OPPORTUNITY_TTL":                 durv(func(c *Config) *time.Duration { return &c.OpportunityTTL }),
	"ARB_OPP_NOTIONAL_USD":                f64(func(c *Config) *float64 { return &c.OpportunityNotionalUSD }),
	"ARB_PERPFUNDING_EXIT_DEBOUNCE":       intv(func(c *Config) *int { return &c.PerpFundExitDebounce }),
	"ARB_PERPFUNDING_MIN_HOLD":            durv(func(c *Config) *time.Duration { return &c.PerpFundMinHold }),
	"ARB_PERP_LEVERAGE_TARGET":            f64(func(c *Config) *float64 { return &c.PerpLeverageTarget }), // D-079：perp 腿目标杠杆
	"ARB_PERP_FUNDING_MIN_NET_ANNUALIZED": f64(func(c *Config) *float64 { return &c.PerpFundingMinNetAnnualized }),
	"ARB_PERP_FUNDING_MIN_NET_STORE":      f64(func(c *Config) *float64 { return &c.PerpFundingMinNetStore }),
	"ARB_POSITION_EVAL_SEC":               intv(func(c *Config) *int { return &c.PositionEvalSec }),
	"ARB_PROBE_ENABLED":                   boolv(func(c *Config) *bool { return &c.ProbeEnabled }),
	"ARB_PROBE_EVAL_DAYS":                 intv(func(c *Config) *int { return &c.ProbeEvalDays }),
	"ARB_PROBE_MAX_NOTIONAL":              f64(func(c *Config) *float64 { return &c.ProbeMaxNotionalUSD }),
	"ARB_PROBE_RPC_ENABLED":               boolv(func(c *Config) *bool { return &c.ProbeRPCEnabled }),
	"ARB_REPO_COLLECT_INTERVAL":           durv(func(c *Config) *time.Duration { return &c.RepoCollectInterval }),
	"ARB_REPO_IDLE_CNY":                   f64(func(c *Config) *float64 { return &c.RepoIdleCny }),
	"ARB_REPO_MIN_RETURN_CNY":             f64(func(c *Config) *float64 { return &c.RepoMinReturnCny }),
	"ARB_REPO_REMINDER_HOUR":              intv(func(c *Config) *int { return &c.RepoReminderHour }),
	"ARB_REPO_SPIKE_CRITICAL_PCT":         f64(func(c *Config) *float64 { return &c.RepoSpikeCriticalPct }),
	"ARB_REPO_SPIKE_WARN_PCT":             f64(func(c *Config) *float64 { return &c.RepoSpikeWarnPct }),
	"ARB_RETRO_ENABLED": {"bool",
		func(c *Config, v string) error {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return err
			}
			c.RetroDisabled = !b
			return nil
		},
		func(c *Config) string { return strconv.FormatBool(!c.RetroDisabled) }, ""},
	"ARB_REVALIDATE_ABS_BPS":             f64(func(c *Config) *float64 { return &c.RevalidateAbsBps }),
	"ARB_REVALIDATE_REL_PCT":             f64(func(c *Config) *float64 { return &c.RevalidateRelPct }),
	"ARB_REVIEW_CARRY_BIAS_ANNUAL_PCT":   f64(func(c *Config) *float64 { return &c.ReviewCarryBiasAnnualPct }),
	"ARB_REVIEW_CROSS_BIAS_BPS":          f64(func(c *Config) *float64 { return &c.ReviewCrossBiasBps }),
	"ARB_REVIEW_MIN_SAMPLES":             intv(func(c *Config) *int { return &c.ReviewMinSamples }),
	"ARB_ROTATE_MAX_PER_DAY":             intv(func(c *Config) *int { return &c.RotateMaxPerDay }),
	"ARB_RISK_DD30_USD":                  f64(func(c *Config) *float64 { return &c.RiskDD30USD }),      // D-032/D-030：30d 账本净额停机线（含事故仓）
	"ARB_RISK_INCIDENT_ACK_ID":           intv(func(c *Config) *int { return &c.RiskIncidentAckID }),   // D-032：事故仓停新开 ack 游标
	"ARB_RISK_CEX_EQUITY_PCT":            f64(func(c *Config) *float64 { return &c.RiskCexEquityPct }), // D-032：CEX 权益纪律闸（%）
	"ARB_RISK_CFD_EQUITY_PCT":            f64(func(c *Config) *float64 { return &c.RiskCfdEquityPct }), // D-032：CFD/MT 权益纪律闸（%）
	"ARB_CAPITAL_BASE_USD":               f64(func(c *Config) *float64 { return &c.CapitalBaseUSD }),   // D-030：经营目标资金基数
	"ARB_OPP_PUBLISH_DELTA_BPS":          f64(func(c *Config) *float64 { return &c.OppPublishDeltaBps }),
	"ARB_SLIPPAGE_BPS":                   f64(func(c *Config) *float64 { return &c.SlippageBps }),
	"ARB_STOP_REENTRY_MINUTES":           intv(func(c *Config) *int { return &c.StopReentryMinutes }),
	"ARB_STORE_MIN_NET_BPS":              f64(func(c *Config) *float64 { return &c.StoreMinNetBps }),
	"ARB_SWAPARB_AUTO_ALLOW":             boolv(func(c *Config) *bool { return &c.SwapArbAutoAllow }),
	"ARB_SWAPARB_BARE_CAP":               f64(func(c *Config) *float64 { return &c.SwapArbBareCap }),
	"ARB_SWAPARB_BASIS_STOP_BPS":         f64(func(c *Config) *float64 { return &c.SwapArbBasisStopBps }),
	"ARB_SWAPARB_COMBO_LOSS_LIMIT":       intv(func(c *Config) *int { return &c.SwapArbComboLossLimit }),
	"ARB_SWAPARB_COMBO_LOSS_WINDOW_DAYS": intv(func(c *Config) *int { return &c.SwapArbComboLossWindowDays }),
	"ARB_SWAPARB_ENTRY_BASIS_MAX_BPS":    f64(func(c *Config) *float64 { return &c.SwapArbEntryBasisMaxBps }),
	"ARB_SWAPARB_ENTRY_SPREAD_MAX_BPS":   f64(func(c *Config) *float64 { return &c.SwapArbEntrySpreadMaxBps }),
	"ARB_SWAPARB_ENTRY_SPREAD_MIN_BPS":   f64(func(c *Config) *float64 { return &c.SwapArbEntrySpreadMinBps }),
	"ARB_SWAPARB_EQUITY_USE_RATIO":       f64(func(c *Config) *float64 { return &c.SwapArbEquityUseRatio }),
	"ARB_SWAPARB_EXIT_NET_BPS":           f64(func(c *Config) *float64 { return &c.SwapArbExitNetBps }),
	"ARB_SWAPARB_MARGIN_BUDGET_PCT":      f64(func(c *Config) *float64 { return &c.SwapArbMarginBudgetPct }),
	"ARB_SWAPARB_MAX_ACTIVE_COMBOS":      intv(func(c *Config) *int { return &c.SwapArbMaxActiveCombos }),
	"ARB_SWAPARB_MAX_GROSS_EXPOSURE":     f64(func(c *Config) *float64 { return &c.SwapArbMaxGrossExposure }),
	"ARB_SWAPARB_MAX_LEGS_PER_SYMBOL":    intv(func(c *Config) *int { return &c.SwapArbMaxLegsPerSymbol }),
	"ARB_SWAPARB_MAX_LEG_SPREAD_BPS":     f64(func(c *Config) *float64 { return &c.SwapArbMaxLegSpreadBps }),
	"ARB_SWAPARB_MAX_PAYBACK_DAYS":       intv(func(c *Config) *int { return &c.SwapArbMaxPaybackDays }),
	"ARB_SWAPARB_MIN_NET_CARRY_BPS":      f64(func(c *Config) *float64 { return &c.SwapArbMinNetCarryBps }),
	"ARB_SWAPARB_REENTRY_COOLDOWN_DAYS":  intv(func(c *Config) *int { return &c.SwapArbReentryCooldownDays }),
	"ARB_SWAPARB_REVIVAL_ALERT_BPS":      f64(func(c *Config) *float64 { return &c.SwapArbRevivalBps }),
	"ARB_SWAPARB_SPEC_DISCOUNT":          f64(func(c *Config) *float64 { return &c.SwapArbSpecDiscount }),
	"ARB_SWAPARB_VENUE_EXPOSURE_CAP":     f64(func(c *Config) *float64 { return &c.SwapArbVenueExposureCap }),
	"ARB_SWAP_DEVIATION_EXIT_PCT":        f64(func(c *Config) *float64 { return &c.SwapDeviationExitPct }),
	"ARB_SWAP_DEVIATION_GATE_PCT":        f64(func(c *Config) *float64 { return &c.SwapDeviationGatePct }),
	"ARB_SWAP_FACTS_ZERO_CHECK":          boolv(func(c *Config) *bool { return &c.SwapFactsZeroCheckEnabled }),
	"ARB_SWAP_FACT_MAX_AGE":              durv(func(c *Config) *time.Duration { return &c.MTData.SwapFactMaxAge }),
	"ARB_SWAP_NON_SETTLE_VENUES":         listv(func(c *Config) *[]string { return &c.SwapNonSettleVenues }),
	"ARB_SWAP_RECONCILE_DEVIATION_PCT":   f64(func(c *Config) *float64 { return &c.SwapReconcileDeviationPct }),
	"ARB_SWAP_REFRESH":                   durv(func(c *Config) *time.Duration { return &c.SwapRefresh }),
	"ARB_SYMBOLS_TTL":                    durv(func(c *Config) *time.Duration { return &c.SymbolsTTL }),
	"ARB_TRADEMODE_TTL":                  durv(func(c *Config) *time.Duration { return &c.TradeModeTTL }),
	"ARB_TRX_ENERGY_RATE_FLOOR_PCT":      f64(func(c *Config) *float64 { return &c.TrxEnergyRateFloorPct }),
	"ARB_TRX_VOTE_BETTER_OPP_PCT":        f64(func(c *Config) *float64 { return &c.TrxVoteBetterOppPct }),
	"ARB_TRX_VOTE_MAX_BROKERAGE":         intv(func(c *Config) *int { return &c.TrxVoteMaxBrokerage }),
	"ARB_TRX_VOTE_MAX_REBAL_PER_DAY":     intv(func(c *Config) *int { return &c.TrxVoteMaxRebalPerDay }),
	"ARB_TRX_VOTE_MAX_SRS":               intv(func(c *Config) *int { return &c.TrxVoteMaxSRs }),
	"ARB_TRX_VOTE_MIN_INTERVAL_MINUTES":  intv(func(c *Config) *int { return &c.TrxVoteMinIntervalMin }),
	"ARB_TRX_VOTE_MIN_SRS":               intv(func(c *Config) *int { return &c.TrxVoteMinSRs }),
	"ARB_TRX_VOTE_SPREAD_THRESHOLD":      f64(func(c *Config) *float64 { return &c.TrxVoteSpreadThreshold }),
	"ARB_TRX_VOTE_YIELD_DROP_PCT":        f64(func(c *Config) *float64 { return &c.TrxVoteYieldDropPct }),
	"ARB_VERIFY_MIN_RATIO":               f64(func(c *Config) *float64 { return &c.VerifyMinRatio }),
	"ARB_VERIFY_MIN_SAMPLES":             intv(func(c *Config) *int { return &c.VerifyMinSamples }),
	"ARB_VERIFY_WINDOW_DAYS":             intv(func(c *Config) *int { return &c.VerifyWindowDays }),
	"ARB_XASSET_CARRY_MIN_NET_STORE":     f64(func(c *Config) *float64 { return &c.XassetCarryMinNetStore }),
	"ARB_XASSET_CEX_FEE_BPS":             f64(func(c *Config) *float64 { return &c.XAssetCexFeeBps }),
	"ARB_XASSET_DISASTER_BPS":            f64(func(c *Config) *float64 { return &c.XAssetDisasterBps }),
	"ARB_XASSET_DISASTER_DEBOUNCE":       intv(func(c *Config) *int { return &c.XAssetDisasterDebounce }),
	"ARB_XASSET_DISASTER_NOISE_MULT":     f64(func(c *Config) *float64 { return &c.XAssetDisasterNoiseMult }),
	"ARB_XASSET_ENTRY_MIN_BPS":           f64(func(c *Config) *float64 { return &c.XassetEntryMinBps }),
	"ARB_XASSET_EXEC_DRIFT_BPS":          f64(func(c *Config) *float64 { return &c.XAssetExecDriftBps }),
	"ARB_XASSET_MIN_NET_ANNUAL_PCT":      f64(func(c *Config) *float64 { return &c.XassetMinNetAnnualPct }),
	"ARB_XASSET_QUOTE_MAX_AGE":           durv(func(c *Config) *time.Duration { return &c.XAssetQuoteMaxAge }),
	"ARB_XASSET_TRACKING_MAX_BPS":        f64(func(c *Config) *float64 { return &c.XassetTrackingMaxBps }),
}

// appConfigEnvKeys 无 Config 字段的标量键（wire/httpx 等直读 os.Getenv）——
// app_config 行 → os.Setenv 注入（须在 Load/消费点前调用）。值非密可进审计行。
var appConfigEnvKeys = []string{
	"ARB_CEX_SCAN_EARN_APR_MIN", "ARB_CEX_SCAN_FUNDING_APR_MIN", "ARB_CEX_SCAN_SPREAD_BPS",
	"ARB_CEX_SYMBOLS", "ARB_CEX_WS_BASE", "ARB_CEX_WS_BASE_BACKUP",
	"ARB_INTRA_BUDGET_USD", "ARB_INTRA_CONCURRENCY", "ARB_INTRA_DAILY_LOSS_PCT",
	"ARB_INTRA_KILL", "ARB_INTRA_MAX_NOTIONAL_USD", "ARB_INTRA_SESSION_GATE",
	"ARB_MT4_GATEWAY", "ARB_OKX_PROXY_URL",
	"ARB_PAPER_AUTHORIZED_MAX_ACTIVE", "ARB_PAPER_AUTHORIZED_REOPEN_COOLDOWN_HOURS", "ARB_PAPER_WINDOW_START",
	"ARB_PAPER_CEX_MIN_DEPTH_USD", "ARB_PAPER_CEX_MIN_FUNDING_PCT", // D-051②：CEX 纸面入口质量闸阈（热读）
	"ARB_SYMDIG_MAX_TOKENS",
	"BINANCE_PROXY_URL", "BINANCE_PROXY_URL_BACKUP",
	"TRONSCAN_BASE", "TRX_GRPC_ENDPOINT", "TRX_RENTAL_PAYERS", "TRX_STAKE_ADDRESS",
}

// parseCSV/parseSet/parseFloatMap 复刻 env helper 语义（空串→nil/空集；坏对跳过）。
func parseCSV(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseSet(v string) map[string]bool {
	out := map[string]bool{}
	for _, p := range parseCSV(v) {
		out[p] = true
	}
	return out
}

func parseFloatMap(v string) map[string]float64 {
	out := map[string]float64{}
	for _, pair := range strings.Split(v, ",") {
		kv := strings.SplitN(strings.TrimSpace(pair), ":", 2)
		if len(kv) != 2 {
			continue
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(kv[1]), 64); err == nil {
			out[strings.TrimSpace(kv[0])] = f
		}
	}
	return out
}
