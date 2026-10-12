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
	OKXProxyURL            string   // ARB_OKX_PROXY_URL
	CexWSBase              string   // ARB_CEX_WS_BASE WS 代理基址（空=直连）
	CexWSBaseBackup        string   // ARB_CEX_WS_BASE_BACKUP
	BinancePerpPublicWSURL string   // ARB_BINANCE_PERP_PUBLIC_WS_URL 逐流覆盖（D-551）
	BinancePerpMarketWSURL string   // ARB_BINANCE_PERP_MARKET_WS_URL
	BinanceSpotWSURL       string   // ARB_BINANCE_SPOT_WS_URL
	BinanceProxyCA         string   // ARB_BINANCE_PROXY_CA 自签 CA 路径（非密）
	MT4Gateway             string   // ARB_MT4_GATEWAY
	CexSymbols             []string // ARB_CEX_SYMBOLS 行情采集符号集
	// === TRX 域 ===
	TrxStakeAddress string // TRX_STAKE_ADDRESS 质押账户地址
	TrxRentalPayers string // TRX_RENTAL_PAYERS 租赁付款方集
	TrxGrpcEndpoint string // TRX_GRPC_ENDPOINT
	TronscanBase    string // TRONSCAN_BASE
	// === 运营旋钮 ===
	IntraBudgetUSD                 float64       // ARB_INTRA_BUDGET_USD
	IntraMaxNotionalUSD            float64       // ARB_INTRA_MAX_NOTIONAL_USD
	IntraDailyLossPct              float64       // ARB_INTRA_DAILY_LOSS_PCT
	IntraConcurrency               int           // ARB_INTRA_CONCURRENCY
	IntraSessionGate               bool          // ARB_INTRA_SESSION_GATE
	IntraKill                      bool          // ARB_INTRA_KILL 急停（重启生效不变）
	ResidualRetryWindow            time.Duration // ARB_RESIDUAL_RETRY_WINDOW（0→装配层 10m）
	ResidualRetryConsec            int           // ARB_RESIDUAL_RETRY_CONSEC（0→装配层 3）
	UniverseFundingMaxAge          time.Duration // ARB_UNIVERSE_FUNDING_MAX_AGE（0→装配层 24h）
	UniverseSwapMaxAge             time.Duration // ARB_UNIVERSE_SWAP_MAX_AGE（0→装配层 48h）
	PaperAuthorizedReopenCooldownH float64       // ARB_PAPER_AUTHORIZED_REOPEN_COOLDOWN_HOURS（<0=未配置→装配层 24h）
	PaperAuthorizedMaxActive       int           // ARB_PAPER_AUTHORIZED_MAX_ACTIVE
	PaperCexMinDepthUSD            float64       // ARB_PAPER_CEX_MIN_DEPTH_USD
	PaperCexMinFundingPct          float64       // ARB_PAPER_CEX_MIN_FUNDING_PCT
	SymdigEnabled                  bool          // ARB_SYMDIG_ENABLED（默认开；0/false 关）
	SymdigMaxTokens                int           // ARB_SYMDIG_MAX_TOKENS（0→symdig 8192）
	SleepmonEnabled                bool          // ARB_SLEEPMON_ENABLED（默认开）
	GateWSMode                     string        // ARB_GATE_WS_MODE（"direct"=原生直连）
	CmcWsDisabled                  bool          // CMC_WS_DISABLE
	DeribitOptEnabled              bool          // ARB_DERIBIT_OPT_ENABLED（默认开）
	OptParityAlertBps              float64       // ARB_OPT_PARITY_ALERT_BPS（0→deribit 15）
	LegTickWindow                  time.Duration // ARB_LEG_TICK_WINDOW（0→30m）
	EgressCanaryAnchors            string        // ARB_EGRESS_CANARY_ANCHORS
	PaperWindowStart               string        // ARB_PAPER_WINDOW_START 观测窗起点（迁移 0173 播种）
	AllowEmptyAuth                 string        // ARB_ALLOW_EMPTY_AUTH 空令牌逃生门（D-159 fail-closed 语义）
}

// loadEnvKnobs env→cfg 兜底层（env 仅是 seed/缺省供值；app_config overlay 在其上覆盖）。
func loadEnvKnobs() EnvKnobs {
	return EnvKnobs{
		BinanceProxyURL:                envStr("BINANCE_PROXY_URL", ""),
		BinanceProxyURLBackup:          envStr("BINANCE_PROXY_URL_BACKUP", ""),
		OKXProxyURL:                    envStr("ARB_OKX_PROXY_URL", ""),
		CexWSBase:                      envStr("ARB_CEX_WS_BASE", ""),
		CexWSBaseBackup:                envStr("ARB_CEX_WS_BASE_BACKUP", ""),
		BinancePerpPublicWSURL:         envStr("ARB_BINANCE_PERP_PUBLIC_WS_URL", ""),
		BinancePerpMarketWSURL:         envStr("ARB_BINANCE_PERP_MARKET_WS_URL", ""),
		BinanceSpotWSURL:               envStr("ARB_BINANCE_SPOT_WS_URL", ""),
		BinanceProxyCA:                 envStr("ARB_BINANCE_PROXY_CA", ""),
		MT4Gateway:                     envStr("ARB_MT4_GATEWAY", ""),
		CexSymbols:                     envStringSlice("ARB_CEX_SYMBOLS"),
		TrxStakeAddress:                envStr("TRX_STAKE_ADDRESS", ""),
		TrxRentalPayers:                envStr("TRX_RENTAL_PAYERS", ""),
		TrxGrpcEndpoint:                envStr("TRX_GRPC_ENDPOINT", ""),
		TronscanBase:                   envStr("TRONSCAN_BASE", ""),
		IntraBudgetUSD:                 envFloat("ARB_INTRA_BUDGET_USD", 0),
		IntraMaxNotionalUSD:            envFloat("ARB_INTRA_MAX_NOTIONAL_USD", 0),
		IntraDailyLossPct:              envFloat("ARB_INTRA_DAILY_LOSS_PCT", 0),
		IntraConcurrency:               envInt("ARB_INTRA_CONCURRENCY", 0),
		IntraSessionGate:               envBool("ARB_INTRA_SESSION_GATE", false),
		IntraKill:                      envBool("ARB_INTRA_KILL", false),
		ResidualRetryWindow:            envDur("ARB_RESIDUAL_RETRY_WINDOW", 0),
		ResidualRetryConsec:            envInt("ARB_RESIDUAL_RETRY_CONSEC", 0),
		UniverseFundingMaxAge:          envDur("ARB_UNIVERSE_FUNDING_MAX_AGE", 0),
		UniverseSwapMaxAge:             envDur("ARB_UNIVERSE_SWAP_MAX_AGE", 0),
		PaperAuthorizedReopenCooldownH: envFloat("ARB_PAPER_AUTHORIZED_REOPEN_COOLDOWN_HOURS", -1),
		PaperAuthorizedMaxActive:       envInt("ARB_PAPER_AUTHORIZED_MAX_ACTIVE", 0),
		PaperCexMinDepthUSD:            envFloat("ARB_PAPER_CEX_MIN_DEPTH_USD", 5000),
		PaperCexMinFundingPct:          envFloat("ARB_PAPER_CEX_MIN_FUNDING_PCT", 10),
		SymdigEnabled:                  envBool("ARB_SYMDIG_ENABLED", true),
		SymdigMaxTokens:                envInt("ARB_SYMDIG_MAX_TOKENS", 0),
		SleepmonEnabled:                envBool("ARB_SLEEPMON_ENABLED", true),
		GateWSMode:                     envStr("ARB_GATE_WS_MODE", ""),
		CmcWsDisabled:                  envBool("CMC_WS_DISABLE", false),
		DeribitOptEnabled:              envBool("ARB_DERIBIT_OPT_ENABLED", true),
		OptParityAlertBps:              envFloat("ARB_OPT_PARITY_ALERT_BPS", 0),
		LegTickWindow:                  envDur("ARB_LEG_TICK_WINDOW", 0),
		EgressCanaryAnchors:            envStr("ARB_EGRESS_CANARY_ANCHORS", ""),
		PaperWindowStart:               envStr("ARB_PAPER_WINDOW_START", ""),
		AllowEmptyAuth:                 envStr("ARB_ALLOW_EMPTY_AUTH", ""),
	}
}
