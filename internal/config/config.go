// Package config 集中管理全链路经济参数（D-035：禁止散落硬编码魔法数字）。
// 全部 env 可覆盖（ARB_* 前缀），默认值单一出处；阈值初值取保守默认，
// 由 D-034 统计复盘以真实成交数据校准。
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 全链路经济参数。
type Config struct {
	// === 检测器阈值（§5，息差型统一为年化百分点，D-036）===
	CrossVenueMinGrossBpsCEX    float64       // CEX(spot/perp) 跨所 gross 门槛（bps）。D-035：cross 降级，仅报价错位捕获
	CrossVenueMinGrossBpsCFD    float64       // CFD(mt4/mt5) 跨所 gross 观察门槛（bps）。低于保本 2.6，展示仍靠评估器 net>0（D-051 WP1）
	FundingMinNetAnnualized     float64       // funding hedge 净年化门槛（%）
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
	CloseBpsThr float64 // 建议平仓：预期净收益 ≤ 该值（bps）

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

	// === 个人持仓实账（财务线工作项 3）===
	LiqMMR float64 // 理论强平维持保证金率（强平价 = entry × (1 + 1/leverage − MMR)；缺省 0.5%）
	// FundingDropRatio 资金费急降预警比例（今日 < 比例×前日 触发；缺省 0.5）——§11 收益侧方向证伪。
	FundingDropRatio float64

	// === 机会站内推送（D-050）===
	// NotifyMinNetBps 机会推送提醒门槛（bps）；0 = 全部新机会都提醒（ARB_NOTIFY_MIN_NET_BPS）。
	NotifyMinNetBps float64

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
	AutoTradeMinNetBps    float64       // 最低净收益门槛 bps（缺省 5.0）
	AutoTradePollInterval time.Duration // OPEN 持仓扫描间隔（缺省 10s）

	// D-072 §3.4 硬编码外部化（默认值=现行为，零迁移）
	AutoTradeAllowVenues       map[string]bool    // ARB_AUTOTRADE_ALLOW_VENUES（默认 277842155:mt5,REDACTED_MT_ACCT:mt5）
	AutoTradeAllowCanonicals   map[string]bool    // ARB_AUTOTRADE_ALLOW_CANONICALS（默认 NAS100,ETHUSDT,XAUUSD,BTCUSDT）
	AutoTradeExcludeCanonicals map[string]bool    // ARB_AUTOTRADE_EXCLUDE_CANONICALS（默认 BRENT,WTI）
	MeanRevertThresholds       map[string]float64 // ARB_MEANREVERT_THRESHOLDS（默认 NAS100:1.5,ETHUSDT:0.5,XAUUSD:0.5）

	// D-072 §3.3/§4.2 swap/funding 退出参数
	AutoTradeMinNetSwapBps float64 // ARB_AUTOTRADE_MIN_NET_SWAP_BPS（默认 0.2）
	SwapArbExitNetBps      float64 // ARB_SWAPARB_EXIT_NET_BPS（默认 0）
	FundingExitNetPct      float64 // ARB_FUNDING_EXIT_NET_PCT（默认 0）
}

// Load 从环境变量加载配置，未设置用默认值。
func Load() *Config {
	return &Config{
		CrossVenueMinGrossBpsCEX:    envFloat("ARB_CROSS_MIN_GROSS_BPS_CEX", 10.0),
		CrossVenueMinGrossBpsCFD:    envFloat("ARB_CROSS_MIN_GROSS_BPS_CFD", 0.1), // D-070 均值回归：极低门槛让 277+591 小价差被检测
		FundingMinNetAnnualized:     envFloat("ARB_FUNDING_MIN_NET_ANNUALIZED", 4.0),
		CarryMinNetAnnualized:       envFloat("ARB_CARRY_MIN_NET_ANNUALIZED", 4.0),
		PerpFundingMinNetAnnualized: envFloat("ARB_PERP_FUNDING_MIN_NET_ANNUALIZED", 2.0),
		GrabMinNetAnnualized:        envFloat("ARB_GRAB_MIN_NET_ANNUALIZED", 4.5),
		DetectorFreshnessTTL:        envDur("ARB_DETECTOR_FRESHNESS_TTL", 120*time.Second), // D-070 均值回归：放宽 freshness 让低频报价 venue 配对
		SlippageBps:                 envFloat("ARB_SLIPPAGE_BPS", 0.3),                     // D-070 均值回归：降低滑点假设（CFD 限价单）
		MarginRateAnnual:            envFloat("ARB_MARGIN_RATE_ANNUAL", 4.5),
		OpportunityTTL:              envDur("ARB_OPPORTUNITY_TTL", 30*time.Second),
		ScanThrottle:                envDur("ARB_SCAN_THROTTLE", 30*time.Second),
		CloseBpsThr:                 envFloat("ARB_CLOSE_BPS_THR", 0.5), // D-070 均值回归：价差回归到 0.5bps 内平仓
		CommissionCFDBps:            envFloat("ARB_COMMISSION_CFD_BPS", 0.3),
		CommissionCEXTakerBps:       envFloat("ARB_COMMISSION_CEX_TAKER_BPS", 10.0),
		CommissionTaker: map[string]float64{
			"binance": envFloat("ARB_COMMISSION_TAKER_BINANCE", 10.0),
			"okx":     envFloat("ARB_COMMISSION_TAKER_OKX", 10.0),
			"gate":    envFloat("ARB_COMMISSION_TAKER_GATE", 10.0),
		},
		SwapRefresh:                envDur("ARB_SWAP_REFRESH", time.Hour),
		TradeModeTTL:               envDur("ARB_TRADEMODE_TTL", 24*time.Hour),
		MaxLegImbalancePct:         envFloat("ARB_MAX_LEG_IMBALANCE_PCT", 2.0),
		ReviewMinSamples:           envInt("ARB_REVIEW_MIN_SAMPLES", 3),
		ReviewCrossBiasBps:         envFloat("ARB_REVIEW_CROSS_BIAS_BPS", 1.0),
		ReviewCarryBiasAnnualPct:   envFloat("ARB_REVIEW_CARRY_BIAS_ANNUAL_PCT", 1.0),
		SymbolsTTL:                 envDur("ARB_SYMBOLS_TTL", 5*time.Minute),
		LiqMMR:                     envFloat("ARB_LIQ_MMR", 0.005),
		FundingDropRatio:           envFloat("ARB_FUNDING_DROP_RATIO", 0.5),
		NotifyMinNetBps:            envFloat("ARB_NOTIFY_MIN_NET_BPS", 0),
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
		AutoTradeAllowCanonicals:   envStringSet("ARB_AUTOTRADE_ALLOW_CANONICALS", "NAS100,ETHUSDT,XAUUSD,BTCUSDT"),
		AutoTradeExcludeCanonicals: envStringSet("ARB_AUTOTRADE_EXCLUDE_CANONICALS", "BRENT,WTI"),
		MeanRevertThresholds:       envFloatMap("ARB_MEANREVERT_THRESHOLDS", "NAS100:1.5,ETHUSDT:0.5,XAUUSD:0.5"),
		AutoTradeMinNetSwapBps:     envFloat("ARB_AUTOTRADE_MIN_NET_SWAP_BPS", 0.2),
		SwapArbExitNetBps:          envFloat("ARB_SWAPARB_EXIT_NET_BPS", 0),
		FundingExitNetPct:          envFloat("ARB_FUNDING_EXIT_NET_PCT", 0),
	}
}

// envStringSet 解析逗号分隔字符串为 set（D-072）。
func envStringSet(key, def string) map[string]bool {
	v := def
	if s, ok := os.LookupEnv(key); ok && s != "" {
		v = s
	}
	out := map[string]bool{}
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out[p] = true
		}
	}
	return out
}

// envFloatMap 解析 key:val,key:val 为 map（D-072）。
func envFloatMap(key, def string) map[string]float64 {
	v := def
	if s, ok := os.LookupEnv(key); ok && s != "" {
		v = s
	}
	out := map[string]float64{}
	for _, pair := range strings.Split(v, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		kv := strings.SplitN(pair, ":", 2)
		if len(kv) != 2 {
			continue
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(kv[1]), 64)
		if err != nil {
			continue
		}
		out[strings.TrimSpace(kv[0])] = f
	}
	return out
}

func envFloat(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
