// Package alerttext 告警出口文本归一+席位转发令组装（D-588/D-622）。叶包零项目依赖
// ——pgstore 告警漏斗与各直推旁路共用；规避 notify→pgstore 反向依赖环。
package alerttext

import (
	"crypto/sha256"
	"encoding/hex"
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

// BannedUntil D-591 S1：从 venue 限频错误文本解封禁到期时戳（banned until <epoch毫秒>
// ——binance -1003/418 响应体原生形态）。user WS 重连退避读封禁窗用（窗内零 REST，
// 防 5s 盲锤续期封禁——S0 实证 04:10:50→04:14:51 延长）。
func BannedUntil(msg string) (time.Time, bool) {
	bm := bannedRe.FindStringSubmatch(msg)
	if bm == nil {
		return time.Time{}, false
	}
	ms, err := strconv.ParseInt(bm[1], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMilli(ms), true
}

// alertDedupNumRe D-592 S1：数字/时戳/时长归一——全部数字段归 <N>
// （age=9h28m43s、至 2026-09-30 11:05:38、HTTP 418、连续 3 次 等同型消化）。
var alertDedupNumRe = regexp.MustCompile(`[0-9]+`)

// AlertDedupKey D-592 S1：告警去重键——message 数字段全归 <N> 后与 level 并合
// sha256（恢复类与故障类文本异形天然异键）。归一化/哈希确定性无错误路径，
// 查询侧 fail-open 由调用方承担。
func AlertDedupKey(level, message string) string {
	h := sha256.Sum256([]byte(level + "|" + alertDedupNumRe.ReplaceAllString(message, "<N>")))
	return hex.EncodeToString(h[:])
}
