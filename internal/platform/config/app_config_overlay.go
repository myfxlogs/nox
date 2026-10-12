// app_config_overlay.go —— D-368 S3-S5：app_config KV overlay（boot 时 DB>env>default）。
// 显式键白名单注册表（防注入面——非白名单键 warn 忽略）；typed setter 解析失败 warn+保留
// env/默认（坏 DB 值不置零不致命）。无热更新 v1：必须在 engine/组件构造前调用（main.go
// config.Load() 之后一行），顺序后移会静默失效。
package config

import (
	"log"
	"os"
	"sort"
)

// ApplyAppConfigOverlay DB 行覆盖 cfg（boot overlay：DB>env>default）。
// 白名单键命中 → typed set + S4 env 残值告警 + S5 source 审计行；DB 无行 → env fallback
// 静默（过渡期合法路径，审计行仍打 source=env/default）；非白名单键 → warn 忽略。
// keys 遍历按字典序（日志确定性）。
func ApplyAppConfigOverlay(cfg *Config, rows map[string]string) {
	keys := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for k := range rows {
		keys = append(keys, k)
		seen[k] = true
	}
	sort.Strings(keys)
	for _, k := range keys {
		setter, ok := appConfigKeys[k]
		if !ok {
			log.Printf("[config] app_config key %s not whitelisted, ignored", k)
			continue
		}
		v := rows[k]
		if err := setter.Set(cfg, v); err != nil {
			log.Printf("[config] %s: invalid app_config value %q ignored (env/default kept): %v", k, v, err)
			continue
		}
		if envV := os.Getenv(k); envV != "" && envV != v {
			log.Printf("[config] %s: env value ignored, app_config row wins", k)
		}
		log.Printf("[config] %s=%s (source=app_config)", k, v)
	}
	// S5 审计行：无 DB 行的键也打 source（env/default），运维一眼看清每个旋钮谁在供值。
	var noRow []string
	for k := range appConfigKeys {
		if !seen[k] {
			noRow = append(noRow, k)
		}
	}
	sort.Strings(noRow)
	for _, k := range noRow {
		source := "default"
		if os.Getenv(k) != "" {
			source = "env"
		}
		log.Printf("[config] %s (source=%s)", k, source)
	}
}

// AppConfigSeedKeys 播种键表=白名单 typed 键（D-515 S4；D-145 死键核后 env-passthrough 轨退役）。
func AppConfigSeedKeys() []string {
	keys := make([]string, 0, len(appConfigKeys))
	for k := range appConfigKeys {
		keys = append(keys, k)
	}
	return keys
}
