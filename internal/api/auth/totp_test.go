package auth

import (
	"net/http"
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
