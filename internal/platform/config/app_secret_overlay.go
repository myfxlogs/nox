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
// NOX_AUTH_TOKEN 自 D-095 起入表（迁 PG 热读，面板热轮换）——显式推翻旧注释
// 「认证域刻意留 env」；D-145：NOX_BINANCE_PROXY_TOKEN 入表（代理链路域密件归 secrets）。
func IsAppSecretKey(k string) bool {
	if strings.HasPrefix(k, "NOX_FEISHU_") || strings.HasPrefix(k, "NOX_AI_") ||
		strings.HasPrefix(k, "LONGBRIDGE_") || strings.HasPrefix(k, "CMC_") ||
		strings.HasPrefix(k, "NOX_CMC_") || strings.HasPrefix(k, "NOX_FRED_") {
		return true
	}
	switch k {
	case "TRX_VOTE_PRIVATE_KEY", "TRONGRID_API_KEY", "NOX_AUTH_TOKEN", "NOX_BINANCE_PROXY_TOKEN":
		return true
	}
	return false
}

// appSecretSeedKeys 播种密钥类 env 键全集（seedConfigFromEnv 扫这些键 env→app_secrets）。
var appSecretSeedKeys = []string{
	"NOX_FEISHU_APP_ID", "NOX_FEISHU_APP_SECRET", "NOX_FEISHU_VERIFY_TOKEN",
	"NOX_AI_BASE_URL", "NOX_AI_API_KEY", "NOX_AI_MODEL", "NOX_AI_PG_DSN",
	"LONGBRIDGE_APP_KEY", "LONGBRIDGE_APP_SECRET", "LONGBRIDGE_ACCESS_TOKEN",
	"CMC_EMAIL", "CMC_PASSWORD", "NOX_CMC_CREATE_TPL",
	"TRX_VOTE_PRIVATE_KEY", "TRONGRID_API_KEY",
	"NOX_AUTH_TOKEN",          // D-095：auth 令牌迁 PG（首启空表播种写回；PG 无值时 resolver env 回落仍可用）
	"NOX_BINANCE_PROXY_TOKEN", // D-145：cex-proxy bearer 迁 PG（secrets 白名单补齐）
}

// AppSecretSeedKeys 返回播种密钥键表。
func AppSecretSeedKeys() []string { return appSecretSeedKeys }

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
