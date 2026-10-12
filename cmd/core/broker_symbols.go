package main

import (
	"os"
	"strconv"
	"strings"

	"nox/internal/api/dashboard"
)

// mt5Config MT5 适配器连接配置。
type mt5Config struct {
	venue  string
	host   string
	server string
	port   int32
	user   int64
	pass   string
}

// loadMT5Config 从环境变量加载 MT5 配置。
// 环境变量：MT5_VENUE, MT5_HOST, MT5_SERVER, MT5_PORT, MT5_USER, MT5_PASS
// D-145 引导件：MT5_*=一次性操作入参（broker_symbols 子命令 attach 凭据），非业务配置——不入 PG。
func loadMT5Config() *mt5Config {
	venue := os.Getenv("MT5_VENUE")
	if venue == "" {
		return nil
	}
	port, _ := strconv.ParseInt(os.Getenv("MT5_PORT"), 10, 32)
	user, _ := strconv.ParseInt(os.Getenv("MT5_USER"), 10, 64)
	return &mt5Config{
		venue:  venue,
		host:   os.Getenv("MT5_HOST"),
		server: os.Getenv("MT5_SERVER"),
		port:   int32(port),
		user:   user,
		pass:   os.Getenv("MT5_PASS"),
	}
}

// brokerSymbols 返回 MT5 订阅的 broker 符号列表。
// 从 MT5_SYMBOLS 环境变量读取，逗号分隔（D-145 引导件：MT5_*=一次性操作入参非业务配置）。
// 默认：EURUSD,GBPUSD,XAUUSD,BTCUSD,ETHUSD
func brokerSymbols(platform string) []string {
	envKey := platform + "_SYMBOLS"
	s := os.Getenv(envKey)
	if s == "" {
		return []string{"EURUSD", "GBPUSD", "XAUUSD", "BTCUSD", "ETHUSD"}
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// symbolMap 构建 brokerSymbol → canonical 映射。
// 初始占位：经 dashboard.Canonicalize 归一到统一 canonical（D-056，BTCUSD→BTCUSDT 等），
// 后续可由 broker_symbol_map DB 行覆盖。
func symbolMap(brokerSymbols []string) map[string]string {
	m := make(map[string]string, len(brokerSymbols))
	for _, s := range brokerSymbols {
		m[s] = dashboard.Canonicalize(s)
	}
	return m
}
