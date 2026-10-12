// Package signutil 中立签名工具叶子包（D-508 S7c：GateSign 自 exec 迁出，
// exec/reconcile/collect 三向共用不再逆向依赖 exec）。
package signutil

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
)

// GateSign Gate API v4 官方签名（D-098 G1，事实已确证勿猜）：
//
//	sign_string = method + "\n" + url + "\n" + query + "\n" + payloadHash + "\n" + timestamp
//	  method      = HTTP 方法大写（POST/GET）
//	  url         = 路径含前缀 /api/v4 + endpoint（如 /api/v4/futures/usdt/orders）
//	  query       = 原始 query string（URL-encoded）；POST 无 query = ""
//	  payloadHash = hex(SHA512(body))；无 body（GET）时 SHA512("")
//	  timestamp   = unix 秒整数（int64），sign_string 与 header 用同一值
//	SIGN = hex(HMAC-SHA512(secret, sign_string))   // 注意是 SHA512 不是 SHA256
func GateSign(secret, method, urlPath, query, body string, ts int64) string {
	payloadHash := hexSHA512(body)
	signString := fmt.Sprintf("%s\n%s\n%s\n%s\n%d", method, urlPath, query, payloadHash, ts)
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write([]byte(signString))
	return hex.EncodeToString(mac.Sum(nil))
}

// hexSHA512 返回 body 的 SHA512 十六进制摘要（空 body → SHA512("")，Gate 签名要求）。
func hexSHA512(body string) string {
	sum := sha512.Sum512([]byte(body))
	return hex.EncodeToString(sum[:])
}
