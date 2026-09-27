// app_secret_overlay.go —— D-515 §1.2：app_secrets 密文通道 boot overlay。
// 密钥类键（FEISHU_*/AI_*/LONGBRIDGE_* 前缀）DB>env：解密后 os.Setenv 注入，
// 必须在 config.Load() 与一切 env 直读消费点（wire_longbridge 等）之前调用。
// 闸门：密键只打 `key (source=app_secrets)` 审计行，值永不进日志。
package config

import (
	"log"
	"os"
	"sort"
	"strings"
)

// IsAppSecretKey 密钥类键判定（app_secrets 通道白名单；标量旋钮走 app_config 永远明文）。
// ARB_AUTH_TOKEN/ARB_BINANCE_PROXY_TOKEN 刻意不在——§4 留 env（基建/认证域）。
func IsAppSecretKey(k string) bool {
	return strings.HasPrefix(k, "ARB_FEISHU_") ||
		strings.HasPrefix(k, "ARB_AI_") ||
		strings.HasPrefix(k, "LONGBRIDGE_")
}

// ApplyAppSecretsOverlay 解密后的 secrets 注入 env（DB>env：DB 行胜出覆盖）。
// 非白名单键 warn 忽略（防注入面）；审计行只打 key 名。
func ApplyAppSecretsOverlay(secrets map[string]string) {
	keys := make([]string, 0, len(secrets))
	for k := range secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !IsAppSecretKey(k) {
			log.Printf("[config] app_secrets key %s not whitelisted, ignored", k)
			continue
		}
		if err := os.Setenv(k, secrets[k]); err != nil {
			log.Printf("[config] app_secrets key %s: setenv failed: %v", k, err)
			continue
		}
		log.Printf("[config] %s (source=app_secrets)", k)
	}
}
