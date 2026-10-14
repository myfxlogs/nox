package signutil

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

// OkxSign OKX V5 官方签名（OK-ACCESS-SIGN）：
//
//	sign_string = timestamp + method + requestPath(+query) + body
//	  timestamp     = ISO8601 毫秒串或 unix 秒小数串（与 header 同值）
//	  requestPath   = 含 /api/v5 前缀与 query（如 /api/v5/account/balance?ccy=USDT）
//	SIGN = base64(HMAC-SHA256(secret, sign_string))
func OkxSign(secret, ts, method, pathWithQuery, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + method + pathWithQuery + body))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
