// config_envknobs.go —— D-145 env 收口 PG：原 env 直读业务旋钮 → Config 字段族
// （嵌入提升字段访问不变，同 XassetConfig/SwapArbRiskConfig 先例）。
// env 只作 boot 种子（seed-once 入库后 PG 唯一真相；DB>env>default 链不动）。
package config

import "time"

// EnvKnobs 原 env 直读键的归一字段族（嵌入 Config，c.X 访问不变）。
type EnvKnobs struct {
	// === 端点/URL 类 ===
	BinanceProxyURL        string   // BINANCE_PROXY_URL cex-proxy 主端点
	BinanceProxyURLBackup  string   // BINANCE_PROXY_URL_BACKUP 备端点（D-519 failover）
	OKXProxyURL            string   // NOX_OKX_PROXY_URL
	CexWSBase              string   // NOX_CEX_WS_BASE WS 代理基址（空=直连）
	CexWSBaseBackup        string   // NOX_CEX_WS_BASE_BACKUP
	BinancePerpPublicWSURL string   // NOX_BINANCE_PERP_PUBLIC_WS_URL 逐流覆盖（D-551）
	BinancePerpMarketWSURL string   // NOX_BINANCE_PERP_MARKET_WS_URL
	BinanceSpotWSURL       string   // NOX_BINANCE_SPOT_WS_URL
	BinanceProxyCA         string   // NOX_BINANCE_PROXY_CA 自签 CA 路径（非密）
	MT4Gateway             string   // NOX_MT4_GATEWAY
	CexSymbols             []string // NOX_CEX_SYMBOLS 行情采集符号集
	// === TRX 域 ===
	TrxStakeAddress string // TRX_STAKE_ADDRESS 质押账户地址
	TrxRentalPayers string // TRX_RENTAL_PAYERS 租赁付款方集
	TrxGrpcEndpoint string // TRX_GRPC_ENDPOINT
	TronscanBase    string // TRONSCAN_BASE
	// === 运营旋钮 ===
	IntraBudgetUSD                 float64       // NOX_INTRA_BUDGET_USD
	IntraMaxNotionalUSD            float64       // NOX_INTRA_MAX_NOTIONAL_USD
	IntraDailyLossPct              float64       // NOX_INTRA_DAILY_LOSS_PCT
	IntraConcurrency               int           // NOX_INTRA_CONCURRENCY
	IntraSessionGate               bool          // NOX_INTRA_SESSION_GATE
	IntraKill                      bool          // NOX_INTRA_KILL 急停（重启生效不变）
	ResidualRetryWindow            time.Duration // NOX_RESIDUAL_RETRY_WINDOW（0→装配层 10m）
	ResidualRetryConsec            int           // NOX_RESIDUAL_RETRY_CONSEC（0→装配层 3）
	UniverseFundingMaxAge          time.Duration // NOX_UNIVERSE_FUNDING_MAX_AGE（0→装配层 24h）
	UniverseSwapMaxAge             time.Duration // NOX_UNIVERSE_SWAP_MAX_AGE（0→装配层 48h）
	PaperAuthorizedReopenCooldownH float64       // NOX_PAPER_AUTHORIZED_REOPEN_COOLDOWN_HOURS（<0=未配置→装配层 24h）
	PaperAuthorizedMaxActive       int           // NOX_PAPER_AUTHORIZED_MAX_ACTIVE
	PaperCexMinDepthUSD            float64       // NOX_PAPER_CEX_MIN_DEPTH_USD
	PaperCexMinFundingPct          float64       // NOX_PAPER_CEX_MIN_FUNDING_PCT
	SymdigEnabled                  bool          // NOX_SYMDIG_ENABLED（默认开；0/false 关）
	SymdigMaxTokens                int           // NOX_SYMDIG_MAX_TOKENS（0→symdig 8192）
	SleepmonEnabled                bool          // NOX_SLEEPMON_ENABLED（默认开）
	GateWSMode                     string        // NOX_GATE_WS_MODE（"direct"=原生直连）
	CmcWsDisabled                  bool          // CMC_WS_DISABLE
	DeribitOptEnabled              bool          // NOX_DERIBIT_OPT_ENABLED（默认开）
	OptParityAlertBps              float64       // NOX_OPT_PARITY_ALERT_BPS（0→deribit 15）
	LegTickWindow                  time.Duration // NOX_LEG_TICK_WINDOW（0→30m）
	EgressCanaryAnchors            string        // NOX_EGRESS_CANARY_ANCHORS
	PaperWindowStart               string        // NOX_PAPER_WINDOW_START 观测窗起点（迁移 0173 播种）
	AllowEmptyAuth                 string        // NOX_ALLOW_EMPTY_AUTH 空令牌逃生门（D-159 fail-closed 语义）
}

// loadEnvKnobs env→cfg 兜底层（env 仅是 seed/缺省供值；app_config overlay 在其上覆盖）。
func loadEnvKnobs() EnvKnobs {
	return EnvKnobs{
		BinanceProxyURL:                envStr("BINANCE_PROXY_URL", ""),
		BinanceProxyURLBackup:          envStr("BINANCE_PROXY_URL_BACKUP", ""),
		OKXProxyURL:                    envStr("NOX_OKX_PROXY_URL", ""),
		CexWSBase:                      envStr("NOX_CEX_WS_BASE", ""),
		CexWSBaseBackup:                envStr("NOX_CEX_WS_BASE_BACKUP", ""),
		BinancePerpPublicWSURL:         envStr("NOX_BINANCE_PERP_PUBLIC_WS_URL", ""),
		BinancePerpMarketWSURL:         envStr("NOX_BINANCE_PERP_MARKET_WS_URL", ""),
		BinanceSpotWSURL:               envStr("NOX_BINANCE_SPOT_WS_URL", ""),
		BinanceProxyCA:                 envStr("NOX_BINANCE_PROXY_CA", ""),
		MT4Gateway:                     envStr("NOX_MT4_GATEWAY", ""),
		CexSymbols:                     envStringSlice("NOX_CEX_SYMBOLS"),
		TrxStakeAddress:                envStr("TRX_STAKE_ADDRESS", ""),
		TrxRentalPayers:                envStr("TRX_RENTAL_PAYERS", ""),
		TrxGrpcEndpoint:                envStr("TRX_GRPC_ENDPOINT", ""),
		TronscanBase:                   envStr("TRONSCAN_BASE", ""),
		IntraBudgetUSD:                 envFloat("NOX_INTRA_BUDGET_USD", 0),
		IntraMaxNotionalUSD:            envFloat("NOX_INTRA_MAX_NOTIONAL_USD", 0),
		IntraDailyLossPct:              envFloat("NOX_INTRA_DAILY_LOSS_PCT", 0),
		IntraConcurrency:               envInt("NOX_INTRA_CONCURRENCY", 0),
		IntraSessionGate:               envBool("NOX_INTRA_SESSION_GATE", false),
		IntraKill:                      envBool("NOX_INTRA_KILL", false),
		ResidualRetryWindow:            envDur("NOX_RESIDUAL_RETRY_WINDOW", 0),
		ResidualRetryConsec:            envInt("NOX_RESIDUAL_RETRY_CONSEC", 0),
		UniverseFundingMaxAge:          envDur("NOX_UNIVERSE_FUNDING_MAX_AGE", 0),
		UniverseSwapMaxAge:             envDur("NOX_UNIVERSE_SWAP_MAX_AGE", 0),
		PaperAuthorizedReopenCooldownH: envFloat("NOX_PAPER_AUTHORIZED_REOPEN_COOLDOWN_HOURS", -1),
		PaperAuthorizedMaxActive:       envInt("NOX_PAPER_AUTHORIZED_MAX_ACTIVE", 0),
		PaperCexMinDepthUSD:            envFloat("NOX_PAPER_CEX_MIN_DEPTH_USD", 5000),
		PaperCexMinFundingPct:          envFloat("NOX_PAPER_CEX_MIN_FUNDING_PCT", 10),
		SymdigEnabled:                  envBool("NOX_SYMDIG_ENABLED", true),
		SymdigMaxTokens:                envInt("NOX_SYMDIG_MAX_TOKENS", 0),
		SleepmonEnabled:                envBool("NOX_SLEEPMON_ENABLED", true),
		GateWSMode:                     envStr("NOX_GATE_WS_MODE", ""),
		CmcWsDisabled:                  envBool("CMC_WS_DISABLE", false),
		DeribitOptEnabled:              envBool("NOX_DERIBIT_OPT_ENABLED", true),
		OptParityAlertBps:              envFloat("NOX_OPT_PARITY_ALERT_BPS", 0),
		LegTickWindow:                  envDur("NOX_LEG_TICK_WINDOW", 0),
		EgressCanaryAnchors:            envStr("NOX_EGRESS_CANARY_ANCHORS", ""),
		PaperWindowStart:               envStr("NOX_PAPER_WINDOW_START", ""),
		AllowEmptyAuth:                 envStr("NOX_ALLOW_EMPTY_AUTH", ""),
	}
}
