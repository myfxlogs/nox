// app_config_meta.go —— D-523 S1：键视图导出层（SettingsPage 后端取数）。
// 分组按白名单真实键前缀归纳（§2 分组映射，真键全列举=前缀规则覆盖全部 156 键）。
package config

import (
	"os"
	"sort"
	"strings"
)

// AppConfigGroup 键分组（值集=设计稿 §2 六组）。
func AppConfigGroup(key string) string {
	switch {
	case strings.HasPrefix(key, "ARB_AUTOTRADE_"), strings.HasPrefix(key, "ARB_INTRA_"),
		strings.HasPrefix(key, "ARB_PAPER_AUTHORIZED_"), key == "ARB_LIVE_TRADE":
		return "AUTOTRADE"
	case strings.HasPrefix(key, "ARB_CARRY_"), strings.HasPrefix(key, "ARB_SWAPARB_"),
		strings.HasPrefix(key, "ARB_PERPFUNDING_"), strings.HasPrefix(key, "ARB_XASSET_"):
		return "CARRY"
	case strings.HasPrefix(key, "ARB_EXPLORER_"):
		return "EXPLORER"
	case strings.HasPrefix(key, "ARB_ANALYST_"):
		return "ANALYST"
	case strings.HasPrefix(key, "ARB_FUNDING_"), strings.HasPrefix(key, "ARB_COMMISSION_"),
		strings.HasPrefix(key, "ARB_FUNDING"):
		return "FUNDING"
	default:
		return "OTHER"
	}
}

// AppConfigAllKeys 全白名单键（typed ∪ env-passthrough，排序）。envPass=true 的键无 Config 字段。
func AppConfigAllKeys() []string {
	out := make([]string, 0, len(appConfigKeys)+len(appConfigEnvKeys))
	for k := range appConfigKeys {
		out = append(out, k)
	}
	out = append(out, appConfigEnvKeys...)
	sort.Strings(out)
	return out
}

// AppConfigKeyMeta 键元数据：typed 键返 (meta,false,true)；env-passthrough 返 ({"string",nil,nil},true,true)；未知 false。
func AppConfigKeyMeta(key string) (AppCfgMeta, bool, bool) {
	if m, ok := appConfigKeys[key]; ok {
		return m, false, true
	}
	for _, k := range appConfigEnvKeys {
		if k == key {
			return AppCfgMeta{Typ: "string"}, true, true
		}
	}
	return AppCfgMeta{}, false, false
}

// SnapshotAppConfigEnv 白名单键 env 快照（boot 期 overlay 注入前调用；0 值键不录）。
func SnapshotAppConfigEnv() map[string]string {
	out := map[string]string{}
	for _, k := range AppConfigAllKeys() {
		if v := os.Getenv(k); v != "" {
			out[k] = v
		}
	}
	return out
}

// LoadSansAppConfigKeys 纯默认 Config：暂摘全部白名单 env → Load → 还原。
// 仅允许在 boot 单线程段调用（env 摘除窗口内无并发读）。
func LoadSansAppConfigKeys() *Config {
	saved := SnapshotAppConfigEnv()
	for k := range saved {
		os.Unsetenv(k)
	}
	c := Load()
	for k, v := range saved {
		os.Setenv(k, v)
	}
	return c
}
