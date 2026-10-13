// Package errclass 分类 MT4/MT5 执行错误为四类 Action（D-077 修复⑩ G3）。
//
// 码表抄 archive internal/errclass/errclass.go（D-077 自审修订②：v1 只做分类落库，
// 不做自动重试——Retry/RetryFresh 留触发条件，待执行失败分类数据积累后评估）。
package errclass

import (
	"strings"

	"nox/internal/data/keydomain"
)

// Action 分类调用方应如何响应 MT4/MT5 错误。
type Action int32

const (
	Retry      Action = iota // 同请求退避重试（v1 不做，仅分类落库）
	RetryFresh               // 换最新价重试（v1 不做，仅分类落库）
	Abort                    // 不重试；计数日志（v1：reason 前缀 Abort:）
	Halt                     // 停策略；市场关闭/交易禁用（v1：DisableStrategy + critical）
)

// String Action 名称（落库 reason 前缀用）。
func (a Action) String() string {
	switch a {
	case Retry:
		return "Retry"
	case RetryFresh:
		return "RetryFresh"
	case Abort:
		return "Abort"
	case Halt:
		return "Halt"
	default:
		return "Abort"
	}
}

// ClassifyMT5 按 MT5 错误码字符串分类（mt5grpc.ErrorCode.String()）。
func ClassifyMT5(code string) Action {
	switch code {
	case "REQUOTE", "PRICE_CHANGED":
		return RetryFresh
	case "OFF_QUOTES", "NO_PRICES", "TOO_MANY_TRADE_REQUESTS":
		return Retry
	case "MARKET_CLOSED", "TRADE_DISABLED":
		return Halt
	case "NO_MONEY", "INVALID_VOLUME", "INVALID_PRICE", "INVALID_STOPS",
		"INVALID_TRADE_VOLUME", "INVALID_TRADE_REQUEST":
		return Abort
	default:
		return Abort
	}
}

// Classify 按 venueKey 前缀路由 MT5 分类（mt4 已退役撤码表；CEX 走 Abort 兜底）。
// code 为错误码字符串（executors 从 ErrorCode.String() 提取）。
func Classify(venueKey, code string) Action {
	switch {
	case keydomain.IsType(venueKey, "mt5"):
		return ClassifyMT5(code)
	default: // mt4 已退役（无码表）
		return Abort // CEX 无标准码表，Abort 兜底
	}
}

// IsFundingRejectMsg D-560：资金拒文判据（autotrade marginWarnThrottled 三子串提为
// 共享件，透出块/dashboard 同调=P3 单源）。liq-distance=强平闸同归资金语义类。
func IsFundingRejectMsg(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "margin insufficient") ||
		strings.Contains(low, "margin is insufficient") ||
		strings.Contains(low, "liq-distance")
}

// TruncateNote 截断 note 到 maxLen 字符（arbcn 同款，防 reason 列超长）。
func TruncateNote(note string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 120
	}
	// rune 计数（中文友好）
	r := []rune(note)
	if len(r) <= maxLen {
		return note
	}
	return string(r[:maxLen]) + "..."
}

// ParseCode 从 executor 错误信息中提取 [code=XXX] 标记（D-077 修复⑩ G3）。
// executors 在错误信息中写入 [code=XXX] 前缀，本函数提取 XXX 供 Classify 分类。
// 无标记 → 返回空串（Classify 兜底 Abort）。
func ParseCode(errMsg string) string {
	const marker = "[code="
	idx := strings.Index(errMsg, marker)
	if idx < 0 {
		return ""
	}
	start := idx + len(marker)
	end := strings.Index(errMsg[start:], "]")
	if end < 0 {
		return ""
	}
	return errMsg[start : start+end]
}

// ClassifyErr 按 venueKey + error 信息分类（D-077 修复⑩ G3 便捷入口）。
// 从 err.Error() 提取 [code=XXX] → Classify。
func ClassifyErr(venueKey string, err error) Action {
	if err == nil {
		return Abort
	}
	return Classify(venueKey, ParseCode(err.Error()))
}
