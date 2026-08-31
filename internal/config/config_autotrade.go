// Package config autotrade 子配置（从 config.go 拆出，文件行数治理）。
// AutotradeConfig 嵌入 Config，字段访问不变（Go embedding promotion）。
package config

import (
	"time"
)

// AutotradeConfig 自动交易相关参数（D-070/D-072/D-159/D-168）。
// 嵌入 Config 后，cfg.AutoTradeEnabled 等访问路径不变。
type AutotradeConfig struct {
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
	// D-168 SWAPARB 自动准入（去白名单化）：AUTO_ALLOW=true 时空白名单=自动放行所有通过门禁栈的组合。
	// 门禁栈（trade_mode/CS/sizing/基差/imbalance/environment）兜底拒绝不适用的组合。
	// DENY_COMBOS 黑名单显式禁止已知问题组合（格式同 ALLOW_COMBOS）。
	// MAX_GROSS_EXPOSURE 全局活跃组合名义合计上限（>0 才校验）。
	SwapArbAutoAllow        bool    // ARB_SWAPARB_AUTO_ALLOW（默认 false，向后兼容）
	SwapArbDenyCombosRaw    string  // ARB_SWAPARB_DENY_COMBOS
	SwapArbMaxGrossExposure float64 // ARB_SWAPARB_MAX_GROSS_EXPOSURE（默认 0=不校验）

	// D-072 §3.3/§4.2 swap/funding 退出参数
	AutoTradeMinNetSwapBps float64 // ARB_AUTOTRADE_MIN_NET_SWAP_BPS（默认 0.2）
	SwapArbExitNetBps      float64 // ARB_SWAPARB_EXIT_NET_BPS（默认 0）
	FundingExitNetPct      float64 // ARB_FUNDING_EXIT_NET_PCT（默认 0）
}

// loadAutotradeConfig 从环境变量加载自动交易子配置，未设置用默认值。
func loadAutotradeConfig() *AutotradeConfig {
	return &AutotradeConfig{
		AutoTradeEnabled:           envBool("ARB_AUTOTRADE_ENABLED", false),
		AutoTradeMaxNotional:       envFloat("ARB_AUTOTRADE_MAX_NOTIONAL", 1000),
		AutoTradeMaxPositions:      envInt("ARB_AUTOTRADE_MAX_POSITIONS", 5),
		AutoTradeMaxDailyLoss:      envFloat("ARB_AUTOTRADE_MAX_DAILY_LOSS", 100),
		AutoTradeMinNetBps:         envFloat("ARB_AUTOTRADE_MIN_NET_BPS", 1.0), // D-070 均值回归：降低门槛让 277+591 波动价差通过
		AutoTradePollInterval:      envDur("ARB_AUTOTRADE_POLL_INTERVAL", 10*time.Second),
		AutoTradeAllowVenues:       envStringSet("ARB_AUTOTRADE_ALLOW_VENUES", "277842155:mt5,REDACTED_MT_ACCT:mt5"),
		AutoTradeAllowCanonicals:   envStringSet("ARB_AUTOTRADE_ALLOW_CANONICALS", ""), // D-122：默认空=全放行（gate 层风控兜底）
		AutoTradeExcludeCanonicals: envStringSet("ARB_AUTOTRADE_EXCLUDE_CANONICALS", "BRENT,WTI"),
		SwapArbAllowCombosRaw:      envStr("ARB_SWAPARB_ALLOW_COMBOS", ""),        // D-159 W1a R1：空=fail-closed
		SwapArbMaxActiveCombos:     envInt("ARB_SWAPARB_MAX_ACTIVE_COMBOS", 1),    // D-138 单组合口径
		SwapArbAutoAllow:           envBool("ARB_SWAPARB_AUTO_ALLOW", false),      // D-168 自动准入
		SwapArbDenyCombosRaw:       envStr("ARB_SWAPARB_DENY_COMBOS", ""),         // D-168 黑名单
		SwapArbMaxGrossExposure:    envFloat("ARB_SWAPARB_MAX_GROSS_EXPOSURE", 0), // D-168 全局额度（0=不校验）
		MeanRevertThresholds:       envFloatMap("ARB_MEANREVERT_THRESHOLDS", "NAS100:1.5,ETHUSDT:0.5,XAUUSD:0.5"),
		AutoTradeMinNetSwapBps:     envFloat("ARB_AUTOTRADE_MIN_NET_SWAP_BPS", 0.2),
		SwapArbExitNetBps:          envFloat("ARB_SWAPARB_EXIT_NET_BPS", 0),
		FundingExitNetPct:          envFloat("ARB_FUNDING_EXIT_NET_PCT", 0),
	}
}
