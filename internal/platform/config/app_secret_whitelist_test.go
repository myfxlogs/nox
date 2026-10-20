package config

import "testing"

// TestAppSecretKeyWhitelist 闸②断言锚：app_secrets 白名单面——
// 已册密钥键过+未册键拒（双表漏键同族病灶防：descs 面错漏在案
// NOX_ALERT_BRIDGE_TOKEN 先证——白名单语义的对抗验证=册外键必拒）。
func TestAppSecretKeyWhitelist(t *testing.T) {
	// 已册密钥键必过（双表对齐在案——descs 可管+白名单放）
	for _, k := range []string{
		"NOX_AUTH_TOKEN", "NOX_TOTP_SECRET", "NOX_ALERT_BRIDGE_TOKEN",
		"NOX_FEISHU_APP_SECRET", "NOX_AI_API_KEY",
	} {
		if !IsAppSecretKey(k) {
			t.Fatalf("已册密钥键 %s 应过白名单", k)
		}
	}
	// 册外键必拒（fail-closed——写面白名单挡未册密钥泄露径）
	for _, k := range []string{
		"NOX_ALERT_BRIDGE_URL", "NOX_RANDOM", "NOX_UNKNOWN_KEY", "",
	} {
		if IsAppSecretKey(k) {
			t.Fatalf("册外键 %q 应拒", k)
		}
	}
}
