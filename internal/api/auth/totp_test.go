package auth

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTOTPAndSession(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	code, err := totpAt(secret, now.Unix()/30)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyTOTP(secret, code, now) {
		t.Fatal("正码应过")
	}
	if VerifyTOTP(secret, "000000", now) && code == "000000" {
		t.Fatal("impossible")
	}
	if VerifyTOTP(secret, "123456", now) && code != "123456" {
		t.Fatal("错码应拒")
	}
	// session：签发→verify 过；exp 后拒；篡 sig 拒
	tok, exp := IssueSession(secret, now)
	if !VerifySession(secret, tok, now) {
		t.Fatal("session 应过")
	}
	if VerifySession(secret, tok, exp.Add(time.Second)) {
		t.Fatal("过期 session 应拒")
	}
	if VerifySession(secret, tok+"x", now) {
		t.Fatal("篡 sig 应拒")
	}
	// 空 secret=径死锁
	if VerifySession("", tok, now) {
		t.Fatal("无 secret 应拒")
	}
}

func TestTOTPLimiter(t *testing.T) {
	l := &TOTPLimiter{}
	now := time.Now()
	for i := 0; i < 5; i++ {
		l.Fail(now)
	}
	if ok, wait := l.Allow(now); ok || wait <= 0 {
		t.Fatalf("连续 5 败应锁定: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.Allow(now.Add(6 * time.Minute)); !ok {
		t.Fatal("锁窗过后应放行")
	}
	l.Reset()
	if ok, _ := l.Allow(now); !ok {
		t.Fatal("Reset 后应放行")
	}
}

// 身份码+QR 标签（业主令 10-19 追加）：6 位稳定小写 hex；归一化（大小写/空白）不变；
// URI 标签 Nox:<身份码>+issuer=Nox+secret 大写。固定向量=python 同源对拍锚
// （sha256("JBSWY3DPEHPK3PXP|idcode").hex()[:6]——跨语言推导一致性钉死）。
func TestOTPAuthURIAndIdCode(t *testing.T) {
	if got, want := TOTPIdCode("JBSWY3DPEHPK3PXP"), "1125ab"; got != want {
		t.Fatalf("身份码固定向量: got %s want %s", got, want)
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	id := TOTPIdCode(secret)
	if len(id) != 6 || id != strings.ToLower(id) {
		t.Fatalf("身份码应 6 位小写 hex: %q", id)
	}
	if TOTPIdCode("  "+strings.ToLower(secret)+"  ") != id {
		t.Fatal("归一化（大小写/空白）后身份码应不变")
	}
	other, _ := GenerateTOTPSecret()
	if TOTPIdCode(other) == id {
		t.Fatal("不同 secret 身份码应不同（160bit 随机碰撞可忽略）")
	}
	uri := OTPAuthURI(secret)
	if !strings.HasPrefix(uri, "otpauth://totp/Nox:"+id+"?") {
		t.Fatalf("URI 标签应 Nox:%s: %s", id, uri)
	}
	if !strings.Contains(uri, "issuer=Nox&") || !strings.Contains(uri, "secret="+strings.ToUpper(secret)) {
		t.Fatalf("URI issuer/secret 应齐: %s", uri)
	}
}

// interceptor session 支：static 不中但 session 中→放行。
func TestInterceptorSessionBranch(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	tok, _ := IssueSession(secret, time.Now())
	i := NewInterceptor(func() string { return "static-tok" }).SetTOTP(func() string { return secret })
	h := http.Header{"Authorization": {"Bearer " + tok}}
	if err := i.check(h); err != nil {
		t.Fatalf("session bearer 应过: %v", err)
	}
	h = http.Header{"Authorization": {"Bearer bogus"}}
	if err := i.check(h); err == nil {
		t.Fatal("伪 session 应拒")
	}
}
