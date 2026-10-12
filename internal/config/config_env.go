// env helper 函数（从 config.go 拆出，D-112 S5.1 文件行数治理）。
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// envStringSlice 解析逗号分隔字符串为切片（D-112；空字符串→nil）。
func envStringSlice(key string) []string {
	s, ok := os.LookupEnv(key)
	if !ok || s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
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

// envStr D-091 分析员：字符串 env（空=未配置）。
func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
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
