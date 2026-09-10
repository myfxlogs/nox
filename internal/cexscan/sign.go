// Package cexscan D-271 CEX 只读扫描器：理财 APR + 跨所价差 + 账户/funding 只读扫描。
//
// sign.go 包内独立签名副本（与 reconcile/exec 同法，各包独立实现，P3 不适用）。
package cexscan

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// binanceSign HMAC-SHA256 hex 签名（与 reconcile/exec 同法，各包独立实现——算法非事实，P3 不适用）。
func binanceSign(secret, query string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}

// okxSign OKX 签名：base64(HMAC-SHA256(secret, prehash))（与 reconcile/exec 同法）。
func okxSign(secret, prehash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(prehash))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
