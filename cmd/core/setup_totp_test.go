// setup_totp_test.go —— TOTP setup/idcode 端点锚（业主令 10-19 追加件）：
// ①setup=Seal 落库+热 Set+otpauth_uri/id_code 同源回传（落库锚=重启即丢缺陷的对抗测试）；
// ②idcode=配置过回 6 位码、未配置 501 fail-closed；③setup 无 Bearer 拒。
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nox/internal/api/auth"
	"nox/internal/platform/config"
	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

func TestTOTPSetupPersistsAndIdCode(t *testing.T) {
	ctx := context.Background()
	st, err := pgstore.New(ctx, testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM app_secrets WHERE key='NOX_TOTP_SECRET'`)
		st.Close()
	})
	t.Setenv("NOX_MASTER_KEY", "totp-setup-test-key")
	secRes := config.NewSecretResolver(map[string]string{"NOX_AUTH_TOKEN": "tok123"})
	e := &totpEndpoints{secRes: secRes, store: st, lim: &auth.TOTPLimiter{}}

	// 无 Bearer 拒。
	w := httptest.NewRecorder()
	e.setup(w, httptest.NewRequest(http.MethodPost, "/totp/setup", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("无 Bearer 应 401: %d", w.Code)
	}

	// 正径：200+otpauth_uri 标签与 id_code 同源。
	r := httptest.NewRequest(http.MethodPost, "/totp/setup", nil)
	r.Header.Set("Authorization", "Bearer tok123")
	w = httptest.NewRecorder()
	e.setup(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("setup 应 200: %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		OTPAuthURI string `json:"otpauth_uri"`
		IdCode     string `json:"id_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.IdCode) != 6 || !strings.HasPrefix(resp.OTPAuthURI, "otpauth://totp/Nox:"+resp.IdCode+"?") {
		t.Fatalf("otpauth_uri/id_code 应同源: %+v", resp)
	}

	// 落库锚（对抗测试：删 Seal 落库段→此断言红=重启即丢缺陷回归）。
	rows, err := st.ListAppSecretRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	has := false
	for _, row := range rows {
		if row.Key == "NOX_TOTP_SECRET" {
			has = true
		}
	}
	if !has {
		t.Fatal("NOX_TOTP_SECRET 应已落库（重启即丢缺陷锚）")
	}

	// 热 Set 锚+idcode 端点同码。
	if secRes.Get("NOX_TOTP_SECRET") == "" {
		t.Fatal("setup 后 resolver 应热读到 secret")
	}
	wi := httptest.NewRecorder()
	e.idcode(wi, httptest.NewRequest(http.MethodGet, "/totp/idcode", nil))
	if wi.Code != http.StatusOK {
		t.Fatalf("idcode 应 200: %d", wi.Code)
	}
	var ic struct {
		IdCode string `json:"id_code"`
	}
	if err := json.Unmarshal(wi.Body.Bytes(), &ic); err != nil {
		t.Fatal(err)
	}
	if ic.IdCode != resp.IdCode {
		t.Fatalf("idcode 端点应与 setup 同码: %s vs %s", ic.IdCode, resp.IdCode)
	}
}

// idcode 未配置=501 fail-closed（登录卡隐去对号提示）。
func TestTOTPIdcodeUnconfigured(t *testing.T) {
	e := &totpEndpoints{secRes: config.NewSecretResolver(nil), lim: &auth.TOTPLimiter{}}
	w := httptest.NewRecorder()
	e.idcode(w, httptest.NewRequest(http.MethodGet, "/totp/idcode", nil))
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("未配置应 501: %d", w.Code)
	}
}
