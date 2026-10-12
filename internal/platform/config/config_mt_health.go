package config

import (
	"os"
	"strings"
	"time"
)

// MTDataConfig MT 数据采集稳定性加固参数（D-104 P0/P2）。
type MTDataConfig struct {
	// HealthTick 健康监控轮询周期（NOX_MT_HEALTH_TICK，缺省 1m）。
	HealthTick time.Duration
	// HealthQuoteWarn/Crit 报价新鲜度 warn/crit 阈值（流断间接探测）。
	HealthQuoteWarn time.Duration // NOX_MT_HEALTH_QUOTE_WARN（缺省 5m）
	HealthQuoteCrit time.Duration // NOX_MT_HEALTH_QUOTE_CRIT（缺省 30m）
	// HealthFactWarn/Crit swap facts 年龄 warn/crit 阈值（漏轮探测；swap=小时级采集）。
	HealthFactWarn time.Duration // NOX_MT_HEALTH_FACT_WARN（缺省 2h=容忍 1 轮漏采）
	HealthFactCrit time.Duration // NOX_MT_HEALTH_FACT_CRIT（缺省 6h=2 轮以上漏采）
	// HealthMarginWarn/Crit margin facts（init_margin_rate=1/Leverage 准静态）年龄阈值——
	// 采集形态=attach 首采+流重连补采（D-006 S1b，无常驻定时器）：流稳定零重连时
	// 数日不写为设计内（10-05 误报实拍：流稳 7h→crit 告警噪音），故独立于 swap 放长。
	HealthMarginWarn time.Duration // NOX_MT_HEALTH_MARGIN_WARN（缺省 24h）
	HealthMarginCrit time.Duration // NOX_MT_HEALTH_MARGIN_CRIT（缺省 72h）
	// SwapFactMaxAge swap facts 新鲜度门禁（discoverSwapCombos fail-closed，NOX_SWAP_FACT_MAX_AGE，缺省 2h）。
	SwapFactMaxAge time.Duration
	// MarginFactMaxAge margin facts 新鲜度门禁（sizing 链 fail-closed，NOX_MARGIN_FACT_MAX_AGE，缺省 2h）。
	// 注：当前 sizing 链无 margin fact 读取点（D-104 S5 核对无挂点），env 预置待未来读取点接入。
	MarginFactMaxAge time.Duration
	// U1：网关节点启动级 failover（默认值=现硬编码）。
	MT5Gateway          string   // NOX_MT5_GATEWAY（缺省 mt5grpc3.mtapi.io:443）
	MT5GatewayFallbacks []string // NOX_MT5_GATEWAY_FALLBACKS（缺省 mt5grpc1,mt5grpc4,mt5grpc2 .mtapi.io:443）
}

// loadMTData 从环境变量加载 MT 数据采集参数，未设置用默认值。
func loadMTData() MTDataConfig {
	return MTDataConfig{
		HealthTick:          envDur("NOX_MT_HEALTH_TICK", time.Minute),         // D-104 P0
		HealthQuoteWarn:     envDur("NOX_MT_HEALTH_QUOTE_WARN", 5*time.Minute), // 流断 5m 间接探测
		HealthQuoteCrit:     envDur("NOX_MT_HEALTH_QUOTE_CRIT", 30*time.Minute),
		HealthFactWarn:      envDur("NOX_MT_HEALTH_FACT_WARN", 2*time.Hour), // 容忍 1 轮漏采
		HealthFactCrit:      envDur("NOX_MT_HEALTH_FACT_CRIT", 6*time.Hour),
		HealthMarginWarn:    envDur("NOX_MT_HEALTH_MARGIN_WARN", 24*time.Hour), // 准静态杠杆（重连驱动写入）
		HealthMarginCrit:    envDur("NOX_MT_HEALTH_MARGIN_CRIT", 72*time.Hour),
		SwapFactMaxAge:      envDur("NOX_SWAP_FACT_MAX_AGE", 2*time.Hour),   // fail-closed 门禁
		MarginFactMaxAge:    envDur("NOX_MARGIN_FACT_MAX_AGE", 2*time.Hour), // 预置（无挂点）
		MT5Gateway:          envStr("NOX_MT5_GATEWAY", "mt5grpc3.mtapi.io:443"),
		MT5GatewayFallbacks: envStringList("NOX_MT5_GATEWAY_FALLBACKS", "mt5grpc1.mtapi.io:443,mt5grpc4.mtapi.io:443,mt5grpc2.mtapi.io:443"),
	}
}

// envStringList 解析逗号分隔字符串为切片（D-104 P2 U1，空串返 nil）。
func envStringList(key, def string) []string {
	v := def
	if s, ok := os.LookupEnv(key); ok {
		v = s
	}
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
