package config

import (
	"os"
	"strings"
	"time"
)

// MTDataConfig MT 数据采集稳定性加固参数（D-104 P0/P2）。
type MTDataConfig struct {
	// HealthTick 健康监控轮询周期（ARB_MT_HEALTH_TICK，缺省 1m）。
	HealthTick time.Duration
	// HealthQuoteWarn/Crit 报价新鲜度 warn/crit 阈值（流断间接探测）。
	HealthQuoteWarn time.Duration // ARB_MT_HEALTH_QUOTE_WARN（缺省 5m）
	HealthQuoteCrit time.Duration // ARB_MT_HEALTH_QUOTE_CRIT（缺省 30m）
	// HealthFactWarn/Crit swap/margin facts 年龄 warn/crit 阈值（漏轮探测）。
	HealthFactWarn time.Duration // ARB_MT_HEALTH_FACT_WARN（缺省 2h=容忍 1 轮漏采）
	HealthFactCrit time.Duration // ARB_MT_HEALTH_FACT_CRIT（缺省 6h=2 轮以上漏采）
	// SwapFactMaxAge swap facts 新鲜度门禁（discoverSwapCombos fail-closed，ARB_SWAP_FACT_MAX_AGE，缺省 2h）。
	SwapFactMaxAge time.Duration
	// MarginFactMaxAge margin facts 新鲜度门禁（sizing 链 fail-closed，ARB_MARGIN_FACT_MAX_AGE，缺省 2h）。
	// 注：当前 sizing 链无 margin fact 读取点（D-104 S5 核对无挂点），env 预置待未来读取点接入。
	MarginFactMaxAge time.Duration
	// D-104 P2 U1：网关节点启动级 failover（默认值=现硬编码）。
	MT5Gateway          string   // ARB_MT5_GATEWAY（缺省 mt5grpc3.mtapi.io:443）
	MT5GatewayFallbacks []string // ARB_MT5_GATEWAY_FALLBACKS（缺省 mt5grpc1,mt5grpc4,mt5grpc2 .mtapi.io:443）
	MT4Gateway          string   // ARB_MT4_GATEWAY（缺省 mt4grpc.mtapi.io:443，无 fallback）
}

// loadMTData 从环境变量加载 MT 数据采集参数，未设置用默认值。
func loadMTData() MTDataConfig {
	return MTDataConfig{
		HealthTick:          envDur("ARB_MT_HEALTH_TICK", time.Minute),         // D-104 P0
		HealthQuoteWarn:     envDur("ARB_MT_HEALTH_QUOTE_WARN", 5*time.Minute), // 流断 5m 间接探测
		HealthQuoteCrit:     envDur("ARB_MT_HEALTH_QUOTE_CRIT", 30*time.Minute),
		HealthFactWarn:      envDur("ARB_MT_HEALTH_FACT_WARN", 2*time.Hour), // 容忍 1 轮漏采
		HealthFactCrit:      envDur("ARB_MT_HEALTH_FACT_CRIT", 6*time.Hour),
		SwapFactMaxAge:      envDur("ARB_SWAP_FACT_MAX_AGE", 2*time.Hour),   // fail-closed 门禁
		MarginFactMaxAge:    envDur("ARB_MARGIN_FACT_MAX_AGE", 2*time.Hour), // 预置（无挂点）
		MT5Gateway:          envStr("ARB_MT5_GATEWAY", "mt5grpc3.mtapi.io:443"),
		MT5GatewayFallbacks: envStringList("ARB_MT5_GATEWAY_FALLBACKS", "mt5grpc1.mtapi.io:443,mt5grpc4.mtapi.io:443,mt5grpc2.mtapi.io:443"),
		MT4Gateway:          envStr("ARB_MT4_GATEWAY", "mt4grpc.mtapi.io:443"),
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
