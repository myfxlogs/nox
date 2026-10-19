// setup_totp.go —— TOTP 登录面（业主令 10-19）：
// POST /totp/login {code} → 验证→4h session 签发（限速：连续 5 败锁 5min）；
// POST /totp/setup（static Bearer 鉴权）→ 生成/重生成 secret Seal 落库 app_secrets
// NOX_TOTP_SECRET+热 Set→回 otpauth_uri+id_code 一次性透出（QR 标签=Nox:身份码，
// 业主令 10-19 追加；secret 不持久透出=重查无径。落库修：纯内存 Set 重启即丢，
// QR 又一次性透出=无从恢复——同票缺陷，UpdateAppSecret 同款 Seal 落库径）；
// GET /totp/idcode → 登录页身份码（无鉴权指纹面，见 handler 注）。
package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"nox/internal/api/auth"
	"nox/internal/platform/config"
	"nox/internal/platform/cryptoutil"
	"nox/internal/store/pgstore"
)

type totpEndpoints struct {
	secRes *config.SecretResolver
	store  *pgstore.Store
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
	// Seal 落库先行（重启不丢；QR 一次性透出后 secret 无处再查）→热 Set 即刻生效。
	// 重生成=旧 secret 即废（session 签名密钥同衍生——全失效）。
	mk, ok := cryptoutil.MasterKey()
	if !ok || mk == nil {
		http.Error(w, "NOX_MASTER_KEY 未配置，无法持久化 totp secret", http.StatusPreconditionFailed)
		return
	}
	enc, err := cryptoutil.Seal(mk, secret)
	if err != nil {
		http.Error(w, "seal secret", http.StatusInternalServerError)
		return
	}
	if err := e.store.UpsertAppSecret(r.Context(), "NOX_TOTP_SECRET", enc, "totp setup 生成/重生成（业主令 10-19）"); err != nil {
		http.Error(w, "persist secret", http.StatusInternalServerError)
		return
	}
	e.secRes.Set("NOX_TOTP_SECRET", secret)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"otpauth_uri": auth.OTPAuthURI(secret),
		"id_code":     auth.TOTPIdCode(secret),
		"note":        "一次性透出——扫码后不可再查；重生成=旧码作废",
	})
}

// idcode 登录页身份码（业主令 10-19 追加）：无鉴权指纹面——只回 6 位 SHA256 衍生码
// （对号用，等价 SSH host key 指纹公开显示）；secret 本体/otpauth_uri 永不回传。
// 未配置=501 与 login 同语义（fail-closed，登录卡隐去对号提示）。
func (e *totpEndpoints) idcode(w http.ResponseWriter, _ *http.Request) {
	secret := e.secRes.Get("NOX_TOTP_SECRET")
	if secret == "" {
		http.Error(w, "totp not configured", http.StatusNotImplemented)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id_code": auth.TOTPIdCode(secret)})
}
