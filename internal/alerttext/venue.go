// Package alerttext 告警出口文本归一（D-588）。叶包零项目依赖——pgstore 告警漏斗
// 与各直推旁路共用；规避 notify→pgstore 反向依赖环。
package alerttext

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// venueErrRe 命中 venue 错误壳「status NNN: {json}」/「http NNN: {json}」——
// tools/xasset-monitor fetchBinanceJSON 与 exec httpStatusError 两种前缀兼容。
var venueErrRe = regexp.MustCompile(`(status|http)\s+(\d{3}):\s*(\{.*)`)

// bannedRe 解 ban 时戳「banned until <epoch毫秒>」。
var bannedRe = regexp.MustCompile(`banned until (\d+)`)

type venueErrBody struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// VenueErrCN 告警文本中 venue 英文错误壳中文化（D-588）。
// 白名单映射——只翻可精确断意的形态，未知形态原样透出（mtMetricCN 同款约定）。
// 只替换错误壳子串，上游业务前缀（如「fetch binance PAXGUSDT:」）保留。
func VenueErrCN(msg string) string {
	m := venueErrRe.FindStringSubmatchIndex(msg)
	if m == nil {
		return msg
	}
	status := msg[m[4]:m[5]]
	var b venueErrBody
	if err := json.Unmarshal([]byte(msg[m[6]:m[7]]), &b); err != nil {
		return msg // 壳内非 JSON → 透出
	}
	var cn string
	if bm := bannedRe.FindStringSubmatch(b.Msg); bm != nil {
		ms, err := strconv.ParseInt(bm[1], 10, 64)
		if err != nil {
			return msg
		}
		cn = fmt.Sprintf("请求频率超限，交易所已临时封禁出口IP至 %s", time.UnixMilli(ms).Format("2006-01-02 15:04:05"))
		if b.Code != 0 {
			cn += fmt.Sprintf("（错误码 %d）", b.Code)
		}
	} else if status == "418" || status == "429" || b.Code == -1003 || b.Code == -1008 {
		cn = fmt.Sprintf("请求频率超限（HTTP %s，错误码 %d）——降低REST频率或改WS", status, b.Code)
	} else {
		return msg // 未识别 status/code 组合 → 宁透不歪
	}
	return msg[:m[0]] + cn + msg[m[1]:]
}
