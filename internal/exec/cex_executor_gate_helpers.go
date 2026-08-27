package exec

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"time"
)

// === Gate（USDT 本位合约 perp，D-098 G1）===

// GateSign Gate API v4 官方签名（D-098 G1，事实已确证勿猜）：
//
//	sign_string = method + "\n" + url + "\n" + query + "\n" + payloadHash + "\n" + timestamp
//	  method      = HTTP 方法大写（POST/GET）
//	  url         = 路径含前缀 /api/v4 + endpoint（如 /api/v4/futures/usdt/orders）
//	  query       = 原始 query string（URL-encoded）；POST 无 query = ""
//	  payloadHash = hex(SHA512(body))；无 body（GET）时 SHA512("")
//	  timestamp   = unix 秒整数（int64），sign_string 与 header 用同一值
//	SIGN = hex(HMAC-SHA512(secret, sign_string))   // 注意是 SHA512 不是 SHA256
//
// 导出自由函数：exec.PlaceOrder（R1.1）与 reconcile.CEXAccountQuerier.accountGate（R1.5）共用，
// P3 一事实一处禁复制两份。reconcile 已 import exec（reconcile_queriers.go），无环。
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

// gateFinishFill 按 finish_as 判定终态订单成交（D-098 返修包① M2）：
//   - finish_as=="filled" → 全成交 Fill（qty=|size-left|，filled 时 left=0 即 |size|）；
//   - finish_as!="filled"（ioc/cancelled/stp/liquidated/reduce_only/position_closed/...）：
//     qty=|size-left|>0 → 如实部分成交 Fill；qty==0 → error 含 finish_as（未成交，防 0-qty 假成交）。
//
// okx 教训：IOC 零成交（finished+finish_as=ioc+left=size）旧逻辑落入 fallthrough 返
// Fill{Qty:0,Price:0},nil → executeLeg 记 FILLED 0-qty 腿。本函数守卫 qty==0 → error。
func gateFinishFill(o gateOrderResponse) (Fill, error) {
	qty, price := gateFilled(o)
	id := strconv.FormatInt(o.ID, 10)
	if o.FinishAs == "filled" {
		return Fill{OrderID: id, Price: price, Qty: qty, Ts: time.Now()}, nil
	}
	if qty > 0 {
		return Fill{OrderID: id, Price: price, Qty: qty, Ts: time.Now()}, nil
	}
	// qty==0：未成交（IOC 零成交 / cancelled / stp / liquidated / ...）→ 如实报错含 finish_as
	return Fill{}, fmt.Errorf("gate order %d: finish_as=%s 未成交（size=%d left=%d）",
		o.ID, o.FinishAs, o.Size, o.Left)
}

// gateFilled 从订单对象计算已成交 qty（|size-left|）与均价（fill_price）。
func gateFilled(o gateOrderResponse) (float64, float64) {
	filled := o.Size - o.Left // 带符号，绝对值即成交合约数
	qty := math.Abs(float64(filled))
	price, _ := strconv.ParseFloat(o.FillPrice, 64)
	return qty, price
}
