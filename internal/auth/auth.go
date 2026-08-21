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
	"net/http"
	"strings"

	"connectrpc.com/connect"
)

// Interceptor 校验 Authorization: Bearer $token（§13.5）。
type Interceptor struct {
	token string
}

// NewInterceptor 创建认证拦截器。
func NewInterceptor(token string) *Interceptor {
	return &Interceptor{token: token}
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
	if i.token == "" {
		return nil
	}
	auth := h.Get("Authorization")
	if auth == "" {
		return errors.New("missing authorization header")
	}
	if !strings.HasPrefix(auth, "Bearer ") {
		return errors.New("invalid authorization scheme")
	}
	if strings.TrimPrefix(auth, "Bearer ") != i.token {
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
