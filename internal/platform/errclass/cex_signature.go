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

// normMsg 败文归一（小写、数字→#、折叠空白、截断）——同坑聚类不撞异坑。
func normMsg(s string, lim int) string {
	s = cexCodeRe.ReplaceAllString(s, "#")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > lim {
		s = strings.TrimSpace(s[:lim])
	}
	return s
}

// VenueRejectSignature 败文→规范签名：
// binance {"code":-1102,"msg":"..."} → "code:-1102|mandatory parameter 'amount'
// was not sent..."（码+归一 msg——binance 同码多因如 -9000「0 decimals」/
// 「refresh product details」不混坑）；无码败文→msg: 归一前缀。
func VenueRejectSignature(errMsg string) string {
	low := strings.ToLower(errMsg)
	var body struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if i := strings.Index(low, "{"); i >= 0 {
		if json.Unmarshal([]byte(low[i:]), &body) == nil && body.Code != 0 {
			if m := normMsg(body.Msg, 40); m != "" {
				return "code:" + strconv.Itoa(body.Code) + "|" + m
			}
			return "code:" + strconv.Itoa(body.Code)
		}
	}
	// HTTP 包装「subscribe HTTP 502: ...」——非 JSON 体整段归一。
	sig := normMsg(low, 60)
	if sig == "" {
		sig = "empty-reject"
	}
	return "msg:" + sig
}
