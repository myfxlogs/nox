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
	"NOX_ALERT_PAUSED":                    boolv(func(c *Config) *bool { return &c.AlertPaused }),
	"NOX_ALERT_RESEND_INTERVAL":           durv(func(c *Config) *time.Duration { return &c.AlertResendInterval }),
	"NOX_ANALYST_RATE_PER_HOUR":           intv(func(c *Config) *int { return &c.AnalystRatePerHour }),
	"NOX_AUTOTRADE_ENABLED":               boolv(func(c *Config) *bool { return &c.AutoTradeEnabled }),
	"NOX_AUTOTRADE_EXCLUDE_CANONICALS":    setv(func(c *Config) *map[string]bool { return &c.AutoTradeExcludeCanonicals }),
	"NOX_AUTOTRADE_MAX_DAILY_LOSS":        f64(func(c *Config) *float64 { return &c.AutoTradeMaxDailyLoss }),
	"NOX_AUTOTRADE_MAX_NOTIONAL":          f64(func(c *Config) *float64 { return &c.AutoTradeMaxNotional }),
	"NOX_AUTOTRADE_MAX_POSITIONS":         intv(func(c *Config) *int { return &c.AutoTradeMaxPositions }),
	"NOX_AUTOTRADE_MIN_NET_BPS":           f64(func(c *Config) *float64 { return &c.AutoTradeMinNetBps }),
	"NOX_AUTOTRADE_MIN_NET_SWAP_BPS":      f64(func(c *Config) *float64 { return &c.AutoTradeMinNetSwapBps }),
	"NOX_AUTOTRADE_POLL_INTERVAL":         durv(func(c *Config) *time.Duration { return &c.AutoTradePollInterval }),
	"NOX_BINANCE_FUTURES_INFO_URL":        strv(func(c *Config) *string { return &c.BinanceFuturesInfoURL }),
	"NOX_BINANCE_SPOT_INFO_URL":           strv(func(c *Config) *string { return &c.BinanceSpotInfoURL }),
	"NOX_BROKER_POS_POLL_MS":              durv(func(c *Config) *time.Duration { return &c.BrokerPosPollInterval }),
	"NOX_CARRY_MIN_NET_ANNUALIZED":        f64(func(c *Config) *float64 { return &c.CarryMinNetAnnualized }),
	"NOX_CARRY_MIN_NET_STORE":             f64(func(c *Config) *float64 { return &c.CarryMinNetStore }),
	"NOX_CARRY_REGIME_FLOOR_PCT":          f64(func(c *Config) *float64 { return &c.CarryRegimeFloorPct }),
	"NOX_CARRY_REGIME_MIN_SAMPLES":        intv(func(c *Config) *int { return &c.CarryRegimeMinSamples }),
	"NOX_CARRY_REGIME_WINDOW_H":           f64(func(c *Config) *float64 { return &c.CarryRegimeWindowH }),
	"NOX_CEX_SCAN_INTERVAL":               durv(func(c *Config) *time.Duration { return &c.CexScanInterval }),
	"NOX_CEX_SCAN_SYMBOLS":                listv(func(c *Config) *[]string { return &c.CexScanSymbols }),
	"NOX_CLOSE_BPS_THR":                   f64(func(c *Config) *float64 { return &c.CloseBpsThr }),
	"NOX_COMBO_LOSS_LIMIT":                intv(func(c *Config) *int { return &c.ComboLossLimit }),
	"NOX_COMBO_LOSS_WINDOW_H":             intv(func(c *Config) *int { return &c.ComboLossWindowH }),
	"NOX_COMMISSION_CEX_TAKER_BPS":        f64(func(c *Config) *float64 { return &c.CommissionCEXTakerBps }),
	"NOX_COMMISSION_CFD_BPS":              f64(func(c *Config) *float64 { return &c.CommissionCFDBps }),
	"NOX_COMMISSION_TAKER_BINANCE":        takerG("binance"),
	"NOX_COMMISSION_TAKER_GATE":           takerG("gate"),
	"NOX_COMMISSION_TAKER_OKX":            takerG("okx"),
	"NOX_CROSS_MIN_GROSS_BPS_CEX":         f64(func(c *Config) *float64 { return &c.CrossVenueMinGrossBpsCEX }),
	"NOX_CROSS_MIN_GROSS_BPS_CFD":         f64(func(c *Config) *float64 { return &c.CrossVenueMinGrossBpsCFD }),
	"NOX_CROSS_NOTIONAL_EQUITY_CAP":       f64(func(c *Config) *float64 { return &c.CrossNotionalEquityCap }),
	"NOX_DETECTOR_FRESHNESS_TTL":          durv(func(c *Config) *time.Duration { return &c.DetectorFreshnessTTL }),
	"NOX_EXPLORER_ENABLED":                boolv(func(c *Config) *bool { return &c.ExplorerEnabled }),
	"NOX_EXIT_FEE_BPS_OKX":                f64(func(c *Config) *float64 { return &c.ExitFeeBpsOKX }),               // D-093 R1：okx 出场 taker 费率（净估计提；0=未配置）
	"NOX_MAKER_MAX_PER_HOUR":              intv(func(c *Config) *int { return &c.MakerMaxAttemptsPerHour }),        // D-018：每 opp 每小时 maker 挂单 attempt 上限（缺=停用）
	"NOX_MT_QUOTE_STALE_MIN":              intv(func(c *Config) *int { return &c.MtQuoteStaleMin }),                // D-029：MT 报价新鲜度阈值分钟（缺=fail-closed 判闭市）
	"NOX_MAKER_TIMEOUT":                   durv(func(c *Config) *time.Duration { return &c.MakerFirstLegTimeout }), // D-018：首腿 post-only 超时 T（缺=停用）
	"NOX_MACRO_ENABLED":                   boolv(func(c *Config) *bool { return &c.MacroEnabled }),
	"NOX_MACRO_BLACKOUT_MIN":              intv(func(c *Config) *int { return &c.MacroBlackoutMin }),           // D-053 ③：high 事件禁区窗 ±分钟
	"NOX_MACRO_BLACKOUT_CURRENCIES":       strv(func(c *Config) *string { return &c.MacroBlackoutCurrencies }), // D-053 ③：禁区币种集
	"NOX_STAGE1_MIN_ROW_HOURS":            intv(func(c *Config) *int { return &c.Stage1MinRowHours }),          // D-061 ①：行存活下限小时
	"NOX_STAGE1_OPEN_WARMUP_H":            intv(func(c *Config) *int { return &c.Stage1OpenWarmupH }),          // D-061 ②：周开盘暖场小时
	"NOX_EXPLORER_RATE_PER_HOUR":          intv(func(c *Config) *int { return &c.ExplorerRatePerHour }),
	"NOX_FREEZE_GUARD":                    durv(func(c *Config) *time.Duration { return &c.FreezeGuard }),
	"NOX_FUNDING_BASELINE_PCT":            f64(func(c *Config) *float64 { return &c.FundingBaselinePct }),
	"NOX_FUNDING_DROP_RATIO":              f64(func(c *Config) *float64 { return &c.FundingDropRatio }),
	"NOX_FUNDING_EXIT_NET_PCT":            f64(func(c *Config) *float64 { return &c.FundingExitNetPct }),
	"NOX_FUNDING_HEDGE_MIN_NET_STORE":     f64(func(c *Config) *float64 { return &c.FundingHedgeMinNetStore }),
	"NOX_FUNDING_MAX_BREAKEVEN_DAYS":      f64(func(c *Config) *float64 { return &c.FundingMaxBreakevenDays }),
	"NOX_MAX_BREAKEVEN_DAYS":              f64(func(c *Config) *float64 { return &c.MaxBreakevenDays }),
	"NOX_HARVEST_HORIZON_DAYS":            f64(func(c *Config) *float64 { return &c.HarvestHorizonDays }),
	"NOX_FUNDING_MIN_NET_ANNUALIZED":      f64(func(c *Config) *float64 { return &c.FundingMinNetAnnualized }),
	"NOX_GRAB_MIN_NET_ANNUALIZED":         f64(func(c *Config) *float64 { return &c.GrabMinNetAnnualized }),
	"NOX_JUDGE_BALANCE_HKD":               f64(func(c *Config) *float64 { return &c.JudgeBalanceHKD }),
	"NOX_JUDGE_BALANCE_USD":               f64(func(c *Config) *float64 { return &c.JudgeBalanceUSD }),
	"NOX_LIQ_MMR":                         f64(func(c *Config) *float64 { return &c.LiqMMR }),
	"NOX_LIVE_TRADE":                      boolv(func(c *Config) *bool { return &c.LiveTrade }),
	"NOX_MARGIN_FACT_MAX_AGE":             durv(func(c *Config) *time.Duration { return &c.MTData.MarginFactMaxAge }),
	"NOX_MARGIN_RATE_ANNUAL":              f64(func(c *Config) *float64 { return &c.MarginRateAnnual }),
	"NOX_MARGIN_RESERVE_PCT":              f64(func(c *Config) *float64 { return &c.MarginReservePct }),
	"NOX_MARGIN_URGENT_COOLDOWN":          durv(func(c *Config) *time.Duration { return &c.MarginUrgentCooldown }),
	"NOX_MARGIN_WARN_COOLDOWN":            durv(func(c *Config) *time.Duration { return &c.MarginWarnCooldown }),
	"NOX_MAX_LEG_IMBALANCE_PCT":           f64(func(c *Config) *float64 { return &c.MaxLegImbalancePct }),
	"NOX_MEANREVERT_THRESHOLDS":           fmapv(func(c *Config) *map[string]float64 { return &c.MeanRevertThresholds }),
	"NOX_MIN_LIQ_DISTANCE_PCT":            f64(func(c *Config) *float64 { return &c.MinLiqDistancePct }),
	"NOX_MT5_GATEWAY":                     strv(func(c *Config) *string { return &c.MTData.MT5Gateway }),
	"NOX_MT5_GATEWAY_FALLBACKS":           listv(func(c *Config) *[]string { return &c.MTData.MT5GatewayFallbacks }),
	"NOX_MT_DISASTER_SL_PCT":              f64(func(c *Config) *float64 { return &c.MTDisasterSlPct }),
	"NOX_MT_HEALTH_FACT_CRIT":             durv(func(c *Config) *time.Duration { return &c.MTData.HealthFactCrit }),
	"NOX_MT_HEALTH_MARGIN_WARN":           durv(func(c *Config) *time.Duration { return &c.MTData.HealthMarginWarn }), // D-067：margin 准静态独立阈
	"NOX_MT_HEALTH_MARGIN_CRIT":           durv(func(c *Config) *time.Duration { return &c.MTData.HealthMarginCrit }), // D-067
	"NOX_MT_HEALTH_FACT_WARN":             durv(func(c *Config) *time.Duration { return &c.MTData.HealthFactWarn }),
	"NOX_MT_HEALTH_QUOTE_CRIT":            durv(func(c *Config) *time.Duration { return &c.MTData.HealthQuoteCrit }),
	"NOX_MT_HEALTH_QUOTE_WARN":            durv(func(c *Config) *time.Duration { return &c.MTData.HealthQuoteWarn }),
	"NOX_MT_HEALTH_TICK":                  durv(func(c *Config) *time.Duration { return &c.MTData.HealthTick }),
	"NOX_OPPORTUNITY_TTL":                 durv(func(c *Config) *time.Duration { return &c.OpportunityTTL }),
	"NOX_OPP_NOTIONAL_USD":                f64(func(c *Config) *float64 { return &c.OpportunityNotionalUSD }),
	"NOX_PERPFUNDING_EXIT_DEBOUNCE":       intv(func(c *Config) *int { return &c.PerpFundExitDebounce }),
	"NOX_PERPFUNDING_MIN_HOLD":            durv(func(c *Config) *time.Duration { return &c.PerpFundMinHold }),
	"NOX_PERP_LEVERAGE_TARGET":            f64(func(c *Config) *float64 { return &c.PerpLeverageTarget }), // D-079：perp 腿目标杠杆
	"NOX_PERP_FUNDING_MIN_NET_ANNUALIZED": f64(func(c *Config) *float64 { return &c.PerpFundingMinNetAnnualized }),
	"NOX_PERP_FUNDING_MIN_NET_STORE":      f64(func(c *Config) *float64 { return &c.PerpFundingMinNetStore }),
	"NOX_POSITION_EVAL_SEC":               intv(func(c *Config) *int { return &c.PositionEvalSec }),
	"NOX_PROBE_ENABLED":                   boolv(func(c *Config) *bool { return &c.ProbeEnabled }),
	"NOX_PROBE_EVAL_DAYS":                 intv(func(c *Config) *int { return &c.ProbeEvalDays }),
	"NOX_PROBE_MAX_COST":                  f64(func(c *Config) *float64 { return &c.ProbeMaxCostUSD }),
	"NOX_PROBE_MAX_NOTIONAL":              f64(func(c *Config) *float64 { return &c.ProbeMaxNotionalUSD }),
	"NOX_PROBE_MAX_OPEN":                  intv(func(c *Config) *int { return &c.ProbeMaxOpen }),
	"NOX_PROBE_RPC_ENABLED":               boolv(func(c *Config) *bool { return &c.ProbeRPCEnabled }),
	"NOX_RETRO_ENABLED": {"bool",
		func(c *Config, v string) error {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return err
			}
			c.RetroDisabled = !b
			return nil
		},
		func(c *Config) string { return strconv.FormatBool(!c.RetroDisabled) }, ""},
	"NOX_REVALIDATE_ABS_BPS":             f64(func(c *Config) *float64 { return &c.RevalidateAbsBps }),
	"NOX_REVALIDATE_REL_PCT":             f64(func(c *Config) *float64 { return &c.RevalidateRelPct }),
	"NOX_REVIEW_CARRY_BIAS_ANNUAL_PCT":   f64(func(c *Config) *float64 { return &c.ReviewCarryBiasAnnualPct }),
	"NOX_REVIEW_CROSS_BIAS_BPS":          f64(func(c *Config) *float64 { return &c.ReviewCrossBiasBps }),
	"NOX_REVIEW_MIN_SAMPLES":             intv(func(c *Config) *int { return &c.ReviewMinSamples }),
	"NOX_ROTATE_MAX_PER_DAY":             intv(func(c *Config) *int { return &c.RotateMaxPerDay }),
	"NOX_RISK_DD30_USD":                  f64(func(c *Config) *float64 { return &c.RiskDD30USD }),      // D-032/D-030：30d 账本净额停机线（含事故仓）
	"NOX_RISK_INCIDENT_ACK_ID":           intv(func(c *Config) *int { return &c.RiskIncidentAckID }),   // D-032：事故仓停新开 ack 游标
	"NOX_RISK_CEX_EQUITY_PCT":            f64(func(c *Config) *float64 { return &c.RiskCexEquityPct }), // D-032：CEX 权益纪律闸（%）
	"NOX_RISK_CFD_EQUITY_PCT":            f64(func(c *Config) *float64 { return &c.RiskCfdEquityPct }), // D-032：CFD/MT 权益纪律闸（%）
	"NOX_CAPITAL_BASE_USD":               f64(func(c *Config) *float64 { return &c.CapitalBaseUSD }),   // D-030：经营目标资金基数
	"NOX_OPP_PUBLISH_DELTA_BPS":          f64(func(c *Config) *float64 { return &c.OppPublishDeltaBps }),
	"NOX_SLIPPAGE_BPS":                   f64(func(c *Config) *float64 { return &c.SlippageBps }),
	"NOX_STOP_REENTRY_MINUTES":           intv(func(c *Config) *int { return &c.StopReentryMinutes }),
	"NOX_STORE_MIN_NET_BPS":              f64(func(c *Config) *float64 { return &c.StoreMinNetBps }),
	"NOX_SWAPARB_AUTO_ALLOW":             boolv(func(c *Config) *bool { return &c.SwapArbAutoAllow }),
	"NOX_SWAPARB_BARE_CAP":               f64(func(c *Config) *float64 { return &c.SwapArbBareCap }),
	"NOX_SWAPARB_BASIS_STOP_BPS":         f64(func(c *Config) *float64 { return &c.SwapArbBasisStopBps }),
	"NOX_SWAPARB_COMBO_LOSS_LIMIT":       intv(func(c *Config) *int { return &c.SwapArbComboLossLimit }),
	"NOX_SWAPARB_COMBO_LOSS_WINDOW_DAYS": intv(func(c *Config) *int { return &c.SwapArbComboLossWindowDays }),
	"NOX_SWAPARB_ENTRY_BASIS_MAX_BPS":    f64(func(c *Config) *float64 { return &c.SwapArbEntryBasisMaxBps }),
	"NOX_SWAPARB_ENTRY_SPREAD_MAX_BPS":   f64(func(c *Config) *float64 { return &c.SwapArbEntrySpreadMaxBps }),
	"NOX_SWAPARB_ENTRY_SPREAD_MIN_BPS":   f64(func(c *Config) *float64 { return &c.SwapArbEntrySpreadMinBps }),
	"NOX_SWAPARB_EQUITY_USE_RATIO":       f64(func(c *Config) *float64 { return &c.SwapArbEquityUseRatio }),
	"NOX_SWAPARB_EXIT_NET_BPS":           f64(func(c *Config) *float64 { return &c.SwapArbExitNetBps }),
	"NOX_SWAPARB_MARGIN_BUDGET_PCT":      f64(func(c *Config) *float64 { return &c.SwapArbMarginBudgetPct }),
	"NOX_SWAPARB_MAX_ACTIVE_COMBOS":      intv(func(c *Config) *int { return &c.SwapArbMaxActiveCombos }),
	"NOX_SWAPARB_MAX_GROSS_EXPOSURE":     f64(func(c *Config) *float64 { return &c.SwapArbMaxGrossExposure }),
	"NOX_SWAPARB_MAX_LEGS_PER_SYMBOL":    intv(func(c *Config) *int { return &c.SwapArbMaxLegsPerSymbol }),
	"NOX_SWAPARB_MAX_LEG_SPREAD_BPS":     f64(func(c *Config) *float64 { return &c.SwapArbMaxLegSpreadBps }),
	"NOX_SWAPARB_MAX_PAYBACK_DAYS":       intv(func(c *Config) *int { return &c.SwapArbMaxPaybackDays }),
	"NOX_SWAPARB_MIN_NET_CARRY_BPS":      f64(func(c *Config) *float64 { return &c.SwapArbMinNetCarryBps }),
	"NOX_SWAPARB_REENTRY_COOLDOWN_DAYS":  intv(func(c *Config) *int { return &c.SwapArbReentryCooldownDays }),
	"NOX_SWAPARB_REVIVAL_ALERT_BPS":      f64(func(c *Config) *float64 { return &c.SwapArbRevivalBps }),
	"NOX_SWAPARB_SPEC_DISCOUNT":          f64(func(c *Config) *float64 { return &c.SwapArbSpecDiscount }),
	"NOX_SWAPARB_VENUE_EXPOSURE_CAP":     f64(func(c *Config) *float64 { return &c.SwapArbVenueExposureCap }),
	"NOX_SWAP_DEVIATION_EXIT_PCT":        f64(func(c *Config) *float64 { return &c.SwapDeviationExitPct }),
	"NOX_SWAP_DEVIATION_GATE_PCT":        f64(func(c *Config) *float64 { return &c.SwapDeviationGatePct }),
	"NOX_SWAP_FACTS_ZERO_CHECK":          boolv(func(c *Config) *bool { return &c.SwapFactsZeroCheckEnabled }),
	"NOX_SWAP_FACT_MAX_AGE":              durv(func(c *Config) *time.Duration { return &c.MTData.SwapFactMaxAge }),
	"NOX_SWAP_NON_SETTLE_VENUES":         listv(func(c *Config) *[]string { return &c.SwapNonSettleVenues }),
	"NOX_SWAP_RECONCILE_DEVIATION_PCT":   f64(func(c *Config) *float64 { return &c.SwapReconcileDeviationPct }),
	"NOX_SWAP_REFRESH":                   durv(func(c *Config) *time.Duration { return &c.SwapRefresh }),
	"NOX_SYMBOLS_TTL":                    durv(func(c *Config) *time.Duration { return &c.SymbolsTTL }),
	"NOX_TRADEMODE_TTL":                  durv(func(c *Config) *time.Duration { return &c.TradeModeTTL }),
	"NOX_TRX_ENERGY_RATE_FLOOR_PCT":      f64(func(c *Config) *float64 { return &c.TrxEnergyRateFloorPct }),
	"NOX_TRX_VOTE_BETTER_OPP_PCT":        f64(func(c *Config) *float64 { return &c.TrxVoteBetterOppPct }),
	"NOX_TRX_VOTE_MAX_BROKERAGE":         intv(func(c *Config) *int { return &c.TrxVoteMaxBrokerage }),
	"NOX_TRX_VOTE_MAX_REBAL_PER_DAY":     intv(func(c *Config) *int { return &c.TrxVoteMaxRebalPerDay }),
	"NOX_TRX_VOTE_MAX_SRS":               intv(func(c *Config) *int { return &c.TrxVoteMaxSRs }),
	"NOX_TRX_VOTE_MIN_INTERVAL_MINUTES":  intv(func(c *Config) *int { return &c.TrxVoteMinIntervalMin }),
	"NOX_TRX_VOTE_MIN_SRS":               intv(func(c *Config) *int { return &c.TrxVoteMinSRs }),
	"NOX_TRX_VOTE_SPREAD_THRESHOLD":      f64(func(c *Config) *float64 { return &c.TrxVoteSpreadThreshold }),
	"NOX_TRX_VOTE_YIELD_DROP_PCT":        f64(func(c *Config) *float64 { return &c.TrxVoteYieldDropPct }),
	"NOX_VERIFY_MIN_RATIO":               f64(func(c *Config) *float64 { return &c.VerifyMinRatio }),
	"NOX_VERIFY_MIN_SAMPLES":             intv(func(c *Config) *int { return &c.VerifyMinSamples }),
	"NOX_VERIFY_WINDOW_DAYS":             intv(func(c *Config) *int { return &c.VerifyWindowDays }),
	"NOX_XASSET_CARRY_MIN_NET_STORE":     f64(func(c *Config) *float64 { return &c.XassetCarryMinNetStore }),
	"NOX_XASSET_CEX_FEE_BPS":             f64(func(c *Config) *float64 { return &c.XAssetCexFeeBps }),
	"NOX_XASSET_DISASTER_BPS":            f64(func(c *Config) *float64 { return &c.XAssetDisasterBps }),
	"NOX_XASSET_DISASTER_DEBOUNCE":       intv(func(c *Config) *int { return &c.XAssetDisasterDebounce }),
	"NOX_XASSET_DISASTER_NOISE_MULT":     f64(func(c *Config) *float64 { return &c.XAssetDisasterNoiseMult }),
	"NOX_XASSET_ENTRY_MIN_BPS":           f64(func(c *Config) *float64 { return &c.XassetEntryMinBps }),
	"NOX_XASSET_FASTMOVE_BPS":            f64(func(c *Config) *float64 { return &c.XAssetFastMoveBps }),
	"NOX_XASSET_FASTMOVE_WINDOW":         durv(func(c *Config) *time.Duration { return &c.XAssetFastMoveWindow }),
	"NOX_XASSET_EXEC_DRIFT_BPS":          f64(func(c *Config) *float64 { return &c.XAssetExecDriftBps }),
	"NOX_XASSET_MIN_NET_ANNUAL_PCT":      f64(func(c *Config) *float64 { return &c.XassetMinNetAnnualPct }),
	"NOX_XASSET_MT_FIRST":                boolv(func(c *Config) *bool { return &c.XAssetMTFirst }), // D-084
	"NOX_XASSET_QUOTE_MAX_AGE":           durv(func(c *Config) *time.Duration { return &c.XAssetQuoteMaxAge }),
	"NOX_XASSET_TRACKING_MAX_BPS":        f64(func(c *Config) *float64 { return &c.XassetTrackingMaxBps }),
	// === D-145 env 收口 PG：原 env 直读键升 typed（app_config 唯一真相，seed-once 播种）===
	"BINANCE_PROXY_URL":                          strv(func(c *Config) *string { return &c.BinanceProxyURL }),
	"BINANCE_PROXY_URL_BACKUP":                   strv(func(c *Config) *string { return &c.BinanceProxyURLBackup }),
	"NOX_OKX_PROXY_URL":                          strv(func(c *Config) *string { return &c.OKXProxyURL }),
	"NOX_CEX_WS_BASE":                            strv(func(c *Config) *string { return &c.CexWSBase }),
	"NOX_CEX_WS_BASE_BACKUP":                     strv(func(c *Config) *string { return &c.CexWSBaseBackup }),
	"NOX_BINANCE_PERP_PUBLIC_WS_URL":             strv(func(c *Config) *string { return &c.BinancePerpPublicWSURL }),
	"NOX_BINANCE_PERP_MARKET_WS_URL":             strv(func(c *Config) *string { return &c.BinancePerpMarketWSURL }),
	"NOX_BINANCE_SPOT_WS_URL":                    strv(func(c *Config) *string { return &c.BinanceSpotWSURL }),
	"NOX_BINANCE_PROXY_CA":                       strv(func(c *Config) *string { return &c.BinanceProxyCA }),
	"NOX_MT4_GATEWAY":                            strv(func(c *Config) *string { return &c.MT4Gateway }),
	"NOX_CEX_SYMBOLS":                            listv(func(c *Config) *[]string { return &c.CexSymbols }),
	"TRX_STAKE_ADDRESS":                          strv(func(c *Config) *string { return &c.TrxStakeAddress }),
	"TRX_RENTAL_PAYERS":                          strv(func(c *Config) *string { return &c.TrxRentalPayers }),
	"TRX_GRPC_ENDPOINT":                          strv(func(c *Config) *string { return &c.TrxGrpcEndpoint }),
	"TRONSCAN_BASE":                              strv(func(c *Config) *string { return &c.TronscanBase }),
	"NOX_INTRA_BUDGET_USD":                       f64(func(c *Config) *float64 { return &c.IntraBudgetUSD }),
	"NOX_INTRA_MAX_NOTIONAL_USD":                 f64(func(c *Config) *float64 { return &c.IntraMaxNotionalUSD }),
	"NOX_INTRA_DAILY_LOSS_PCT":                   f64(func(c *Config) *float64 { return &c.IntraDailyLossPct }),
	"NOX_INTRA_CONCURRENCY":                      intv(func(c *Config) *int { return &c.IntraConcurrency }),
	"NOX_INTRA_SESSION_GATE":                     boolv(func(c *Config) *bool { return &c.IntraSessionGate }),
	"NOX_INTRA_KILL":                             boolv(func(c *Config) *bool { return &c.IntraKill }),
	"NOX_RESIDUAL_RETRY_WINDOW":                  durv(func(c *Config) *time.Duration { return &c.ResidualRetryWindow }),
	"NOX_RESIDUAL_RETRY_CONSEC":                  intv(func(c *Config) *int { return &c.ResidualRetryConsec }),
	"NOX_UNIVERSE_FUNDING_MAX_AGE":               durv(func(c *Config) *time.Duration { return &c.UniverseFundingMaxAge }),
	"NOX_UNIVERSE_SWAP_MAX_AGE":                  durv(func(c *Config) *time.Duration { return &c.UniverseSwapMaxAge }),
	"NOX_PAPER_AUTHORIZED_REOPEN_COOLDOWN_HOURS": f64(func(c *Config) *float64 { return &c.PaperAuthorizedReopenCooldownH }),
	"NOX_PAPER_AUTHORIZED_MAX_ACTIVE":            intv(func(c *Config) *int { return &c.PaperAuthorizedMaxActive }),
	"NOX_PAPER_CEX_MIN_DEPTH_USD":                f64(func(c *Config) *float64 { return &c.PaperCexMinDepthUSD }),
	"NOX_PAPER_CEX_MIN_FUNDING_PCT":              f64(func(c *Config) *float64 { return &c.PaperCexMinFundingPct }),
	"NOX_SYMDIG_ENABLED":                         boolv(func(c *Config) *bool { return &c.SymdigEnabled }),
	"NOX_SYMDIG_MAX_TOKENS":                      intv(func(c *Config) *int { return &c.SymdigMaxTokens }),
	"NOX_SLEEPMON_ENABLED":                       boolv(func(c *Config) *bool { return &c.SleepmonEnabled }),
	"NOX_GATE_WS_MODE":                           strv(func(c *Config) *string { return &c.GateWSMode }),
	"NOX_DERIBIT_OPT_ENABLED":                    boolv(func(c *Config) *bool { return &c.DeribitOptEnabled }),
	"NOX_OPT_PARITY_ALERT_BPS":                   f64(func(c *Config) *float64 { return &c.OptParityAlertBps }),
	"NOX_LEG_TICK_WINDOW":                        durv(func(c *Config) *time.Duration { return &c.LegTickWindow }),
	"NOX_EGRESS_CANARY_ANCHORS":                  strv(func(c *Config) *string { return &c.EgressCanaryAnchors }),
	"NOX_PAPER_WINDOW_START":                     strv(func(c *Config) *string { return &c.PaperWindowStart }),
	"NOX_ALLOW_EMPTY_AUTH":                       strv(func(c *Config) *string { return &c.AllowEmptyAuth }),
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
