// totp_secret_channel_test.go —— 业主令 10-19 追加件锚（同 D-095 形态）：
// NOX_TOTP_SECRET 入 app_secrets 通道。病灶实录：dashboard descs 补了键而 config
// 白名单/播种集漏列——resolver Get/Set 对该键静默 no-op，login 恒 501、setup 恒丢
// （展示副本假象）。此测试=对抗锚：再漏任一处即红。
package config

import "testing"

func TestTOTPSecretResolverChannel(t *testing.T) {
	if !IsAppSecretKey("NOX_TOTP_SECRET") {
		t.Fatal("NOX_TOTP_SECRET 应入 app_secrets 白名单（漏列=resolver 静默 no-op）")
	}
	found := false
	for _, k := range AppSecretSeedKeys() {
		if k == "NOX_TOTP_SECRET" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("NOX_TOTP_SECRET 应入播种键集")
	}
	// resolver 通道语义：Set 即效→Get 读回（login/idcode/setup 热读径依赖）。
	r := NewSecretResolver(nil)
	r.Set("NOX_TOTP_SECRET", "JBSWY3DPEHPK3PXP")
	if got := r.Get("NOX_TOTP_SECRET"); got != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("Set 后应热读回: got %q", got)
	}
}
