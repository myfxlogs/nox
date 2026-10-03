// okx_private_ws_login.go —— OKX 私有流 login 帧/签名助手（自 okx_private_ws.go 整搬，
// 纯搬家零语义——保 ≤300 行门禁；凭据只进帧不进日志，B4 锚）。
package exchange

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"time"
)

// okxPrivateLoginTs login timestamp 串（R3 返修：SSOT §5 B.2 订正=Unix epoch 秒串；
// 原 ISO8601 系 REST 头格式误植——OKX WS login 60004 Invalid timestamp 在产实证）。
func okxPrivateLoginTs(now time.Time) string {
	return strconv.FormatInt(now.Unix(), 10)
}

// okxPrivateLoginPrehash OKX login 签名原串（SSOT §5 B.2：ts+"GET"+"/users/self/verify"）。
// R1 返修抽取：调用点经此构造 prehash，锚钉组合而非只测纯函数。
func okxPrivateLoginPrehash(ts string) string {
	return ts + "GET" + "/users/self/verify"
}

// loginFrame 组装完整 login 帧（含签名）。
func (o *OKXPrivateWS) loginFrame(ts string) map[string]any {
	return okxPrivateLoginFrame(o.apiKey, o.passphrase, ts, okxPrivateSign(o.apiSecret, okxPrivateLoginPrehash(ts)))
}

// okxPrivateSign OKX 签名（与 reconcile.okxSign 同算法：base64(HMAC-SHA256(secret, prehash))）。
func okxPrivateSign(secret, prehash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(prehash))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// okxPrivateLoginFrame login 帧（SSOT §5 B.2 逐字；凭据只进帧不进日志，B4 锚）。
func okxPrivateLoginFrame(apiKey, passphrase, ts, sign string) map[string]any {
	return map[string]any{
		"op": "login",
		"args": []map[string]string{{
			"apiKey":     apiKey,
			"passphrase": passphrase,
			"timestamp":  ts,
			"sign":       sign,
		}},
	}
}
