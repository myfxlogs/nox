// setup_totp.go —— TOTP 登录面（业主令 10-19）：
// POST /totp/login {code} → 验证→4h session 签发（限速：连续 5 败锁 5min）；
// POST /totp/setup（static Bearer 鉴权）→ 生成/重生成 secret 存 app_secrets
// NOX_TOTP_SECRET→回 otpauth_uri 一次性透出（票面「CLI 或一次性 RPC」支：
// 业主侧 qrencode/Authenticator 手动输入渲染 QR；secret 不持久透出=重查无径）。
package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"nox/internal/api/auth"
	"nox/internal/platform/config"
)

type totpEndpoints struct {
	secRes *config.SecretResolver
	lim    *auth.TOTPLimiter
}

func (e *totpEndpoints) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	now := time.Now()
	if ok, wait := e.lim.Allow(now); !ok {
		http.Error(w, "locked, retry after "+wait.Round(time.Second).String(), http.StatusTooManyRequests)
		return
	}
	secret := e.secRes.Get("NOX_TOTP_SECRET")
	if secret == "" {
		http.Error(w, "totp not configured", http.StatusNotImplemented)
		return
	}
	if !auth.VerifyTOTP(secret, req.Code, now) {
		e.lim.Fail(now)
		http.Error(w, "invalid code", http.StatusUnauthorized)
		return
	}
	e.lim.Reset()
	tok, exp := auth.IssueSession(secret, now)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":      tok,
		"expires_at": exp.UTC().Format(time.RFC3339),
	})
}

func (e *totpEndpoints) setup(w http.ResponseWriter, r *http.Request) {
	// static Bearer 鉴权（AI 席位/业主 CLI 用——NOX_AUTH_TOKEN 热读同 interceptor）。
	token := e.secRes.Get("NOX_AUTH_TOKEN")
	ah := r.Header.Get("Authorization")
	if token == "" || !strings.HasPrefix(ah, "Bearer ") || strings.TrimPrefix(ah, "Bearer ") != token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		http.Error(w, "gen secret", http.StatusInternalServerError)
		return
	}
	e.secRes.Set("NOX_TOTP_SECRET", secret) // 重生成=旧 secret 即废（session 签名密钥同衍生——全失效）
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"otpauth_uri": auth.OTPAuthURI(secret),
		"note":        "一次性透出——扫码后不可再查；重生成=旧码作废",
	})
}
