// Package config 集中管理全链路经济参数（D-035：禁止散落硬编码魔法数字）。
// 全部 env 可覆盖（ARB_* 前缀），默认值单一出处；阈值初值取保守默认，
// 由 D-034 统计复盘以真实成交数据校准。
package config

import (
	"os"
	"strconv"
	"time"
)

// Config 全链路经济参数。
type Config struct {
	// === 检测器阈值（§5）===
	CrossVenueMinGrossBps   float64       // 跨所 gross 门槛（bps）。D-035：cross 降级，仅报价错位捕获
	FundingMinNetAnnualized float64       // funding hedge 净年化门槛（%）
	CarryMinNetBps          float64       // carry 净年化门槛（bps）
	DetectorFreshnessTTL    time.Duration // 报价陈旧排除 TTL

	// === 评估器成本（§6）===
	SlippageBps      float64       // 单腿滑点（bps）
	MarginRateAnnual float64       // 资金成本年化（%）
	OpportunityTTL   time.Duration // 机会过期 TTL

	// === 持仓监控（§13.4）===
	CloseBpsThr float64 // 建议平仓：预期净收益 ≤ 该值（bps）

	// === 佣金（bps/腿）——真实定价待 per-venue 配置化（broker_accounts 账号类型）===
	CommissionCFDBps      float64 // MT4/MT5 单腿
	CommissionCEXTakerBps float64 // CEX taker 单腿（binance/okx/gate 统一；逐交易所细化待后续）
}

// Load 从环境变量加载配置，未设置用默认值。
func Load() *Config {
	return &Config{
		CrossVenueMinGrossBps:   envFloat("ARB_CROSS_MIN_GROSS_BPS", 10.0),
		FundingMinNetAnnualized: envFloat("ARB_FUNDING_MIN_NET_ANNUALIZED", 4.0),
		CarryMinNetBps:          envFloat("ARB_CARRY_MIN_NET_BPS", 8.0),
		DetectorFreshnessTTL:    envDur("ARB_DETECTOR_FRESHNESS_TTL", 60*time.Second),
		SlippageBps:             envFloat("ARB_SLIPPAGE_BPS", 1.0),
		MarginRateAnnual:        envFloat("ARB_MARGIN_RATE_ANNUAL", 4.5),
		OpportunityTTL:          envDur("ARB_OPPORTUNITY_TTL", 30*time.Second),
		CloseBpsThr:             envFloat("ARB_CLOSE_BPS_THR", 2.0),
		CommissionCFDBps:        envFloat("ARB_COMMISSION_CFD_BPS", 0.3),
		CommissionCEXTakerBps:   envFloat("ARB_COMMISSION_CEX_TAKER_BPS", 10.0),
	}
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
