package errclass

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// cex_signature.go —— D-255：CEX 拒因规范化签名（fin_venue_reject.error_signature
// 单源）。码表在 errclass（P3）——dciexec 写侧/读侧共调，白名单冻结不引新边。

var cexCodeRe = regexp.MustCompile(`\d+`)

// VenueRejectSignature 败文→规范签名：
// binance {"code":-1102,"msg":"..."} → "code:-1102"；
// 无码败文→归一前缀（小写、数字→#、截 60 字符）——同坑聚类不同坑不撞。
func VenueRejectSignature(errMsg string) string {
	low := strings.ToLower(errMsg)
	// JSON {"code":N,"msg":"..."} 先解（签名取码不取 msg——msg 值尾噪声大）。
	var body struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if i := strings.Index(low, "{"); i >= 0 {
		if json.Unmarshal([]byte(low[i:]), &body) == nil && body.Code != 0 {
			return "code:" + strconv.Itoa(body.Code)
		}
	}
	// HTTP 包装「subscribe HTTP 502: ...」——非 JSON 体整段归一。
	sig := cexCodeRe.ReplaceAllString(low, "#")
	sig = strings.Join(strings.Fields(sig), " ")
	if len(sig) > 60 {
		sig = sig[:60]
	}
	if sig == "" {
		sig = "empty-reject"
	}
	return "msg:" + sig
}
