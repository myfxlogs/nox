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
	// === 检测器阈值（§5，息差型统一为年化百分点，D-036）===
	CrossVenueMinGrossBps   float64       // 跨所 gross 门槛（bps）。D-035：cross 降级，仅报价错位捕获
	FundingMinNetAnnualized float64       // funding hedge 净年化门槛（%）
	CarryMinNetAnnualized   float64       // carry 净年化门槛（%）——D-036 由 bps 改百分点，与 funding 对齐
	GrabMinNetAnnualized    float64       // grab 档门槛（%），替代 classify 硬编码 4.5（D-036）
	DetectorFreshnessTTL    time.Duration // 报价陈旧排除 TTL

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
}

// Load 从环境变量加载配置，未设置用默认值。
func Load() *Config {
	return &Config{
		CrossVenueMinGrossBps:   envFloat("ARB_CROSS_MIN_GROSS_BPS", 10.0),
		FundingMinNetAnnualized: envFloat("ARB_FUNDING_MIN_NET_ANNUALIZED", 4.0),
		CarryMinNetAnnualized:   envFloat("ARB_CARRY_MIN_NET_ANNUALIZED", 4.0),
		GrabMinNetAnnualized:    envFloat("ARB_GRAB_MIN_NET_ANNUALIZED", 4.5),
		DetectorFreshnessTTL:    envDur("ARB_DETECTOR_FRESHNESS_TTL", 60*time.Second),
		SlippageBps:             envFloat("ARB_SLIPPAGE_BPS", 1.0),
		MarginRateAnnual:        envFloat("ARB_MARGIN_RATE_ANNUAL", 4.5),
		OpportunityTTL:          envDur("ARB_OPPORTUNITY_TTL", 30*time.Second),
		ScanThrottle:            envDur("ARB_SCAN_THROTTLE", 30*time.Second),
		CloseBpsThr:             envFloat("ARB_CLOSE_BPS_THR", 2.0),
		CommissionCFDBps:        envFloat("ARB_COMMISSION_CFD_BPS", 0.3),
		CommissionCEXTakerBps:   envFloat("ARB_COMMISSION_CEX_TAKER_BPS", 10.0),
		CommissionTaker: map[string]float64{
			"binance": envFloat("ARB_COMMISSION_TAKER_BINANCE", 10.0),
			"okx":     envFloat("ARB_COMMISSION_TAKER_OKX", 10.0),
			"gate":    envFloat("ARB_COMMISSION_TAKER_GATE", 10.0),
		},
		SwapRefresh: envDur("ARB_SWAP_REFRESH", time.Hour),
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
