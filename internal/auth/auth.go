// Package auth 实现认证 interceptor + 凭据加解密（§13.5）。
package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"connectrpc.com/connect"
)

// ValidateStartupToken 启动时校验 auth token 配置（D-159 W0 P0-6 fail-fast 过渡补丁）。
// D-095：token=有效令牌（app_secrets DB 值∪env 回落，调用方经 SecretResolver 取）；
// 双空且未显式允许逃生门（allowEmpty != "true"）→ 返回错误（调用方 log.Fatal）。
// 双空且允许逃生门 → 大声警告（本地调试唯一合法路径）。
// token 非空 → 正常放行。
func ValidateStartupToken(token, allowEmpty string) error {
	if token != "" {
		return nil
	}
	if allowEmpty == "true" {
		log.Printf("[auth] WARNING: ARB_ALLOW_EMPTY_AUTH=true — auth disabled (local debug only, DO NOT use in production)")
		return nil
	}
	return errors.New("ARB_AUTH_TOKEN is empty (app_secrets row and env fallback both unset) and ARB_ALLOW_EMPTY_AUTH is not 'true' — refusing to start (set it via app_secrets panel or env, or explicitly allow empty auth for local debug)")
}

// Interceptor 校验 Authorization: Bearer $token（§13.5）。
// D-095：令牌热读——tokenFn 每请求取当前有效令牌（main 装配闭包经 SecretResolver
// 读 app_secrets DB 值∪env 回落；面板改后写时同步 Set 即效，旧令牌即失效）。
type Interceptor struct {
	tokenFn func() string
}

// NewInterceptor 创建认证拦截器（getToken=每请求调用的当前令牌读口；nil 视同恒空=fail-closed）。
func NewInterceptor(getToken func() string) *Interceptor {
	return &Interceptor{tokenFn: getToken}
}

// WrapUnary 返回 connect.UnaryInterceptorFunc。
func (i *Interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := i.check(req.Header()); err != nil {
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient 客户端侧（不需要）。
func (i *Interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler 服务端 streaming 拦截。
func (i *Interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, w connect.StreamingHandlerConn) error {
		if err := i.check(w.RequestHeader()); err != nil {
			return connect.NewError(connect.CodeUnauthenticated, err)
		}
		return next(ctx, w)
	}
}

func (i *Interceptor) check(h http.Header) error {
	// D-159 W6a S1：移除 token=="" 逃生门（生产 token 必须非空，ValidateStartupToken 已保证启动时非空或显式 allowEmpty）。
	// D-095：热读每请求取当前令牌；空（PG+env 双空/读口 nil）=未配置认证 → fail-closed 拒绝所有请求（禁放行）。
	token := ""
	if i.tokenFn != nil {
		token = i.tokenFn()
	}
	if token == "" {
		return errors.New("auth token not configured (ARB_AUTH_TOKEN empty)")
	}
	auth := h.Get("Authorization")
	if auth == "" {
		return errors.New("missing authorization header")
	}
	if !strings.HasPrefix(auth, "Bearer ") {
		return errors.New("invalid authorization scheme")
	}
	if strings.TrimPrefix(auth, "Bearer ") != token {
		return errors.New("invalid token")
	}
	return nil
}

// EncryptCredentials 用 AES-256-GCM 加密凭据（§13.5）。
func EncryptCredentials(masterKey, plaintext []byte) ([]byte, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes, got %d", len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// DecryptCredentials 用 AES-256-GCM 解密凭据。
func DecryptCredentials(masterKey, ciphertext []byte) ([]byte, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes, got %d", len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, ct, nil)
}
