package swapledger

import (
	"log"
	"strings"
	"time"

	"arb/internal/decimalutil"
)

// factKey (canonical, venue) → swap/funding rate。
type factKey struct {
	canonical, venue string
}

// venueFromKey 从 venueKey（格式 "venueName:mt5"）提取 venue 名。
func venueFromKey(venueKey string) string {
	idx := strings.Index(venueKey, ":")
	if idx < 0 {
		return venueKey
	}
	return venueKey[:idx]
}

// canonicalFromOppID 从 opportunity_id 解析 canonical（格式 PREFIX:SYMBOL:...）。
func canonicalFromOppID(oppID string) string {
	parts := strings.Split(oppID, ":")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// cashflowKind 按持仓类型返回 cashflow kind（swap 或 funding）。
func cashflowKind(posType string) string {
	if isFundingType(posType) {
		return "funding"
	}
	return "swap"
}

// isFundingType 判断是否为 funding 类持仓（8h×3 周期）。
func isFundingType(posType string) bool {
	for _, t := range FundingPositionTypes {
		if t == posType {
			return true
		}
	}
	return false
}

// settleMultiplier 返回当日结算乘数（D-077 V2→修复⑪A M3+M4）。
// 优先级：symbol 级 Fact > venue 级 Fact > fail-closed（跳过+warn，禁止回退已知错误值）。
// 三倍日→3 否则 1。TripleDayResolver 未知（ok=false）→ 返回 0（调用方跳过该腿记账）。
func settleMultiplier(venueKey, canonical string, day time.Time, r TripleDayResolver) float64 {
	venueName := venueFromKey(venueKey)
	tripleDay, ok := r.TripleSwapDay(venueName, canonical)
	if !ok {
		// D-077 修复⑪A M4：删周三回退 → fail-closed（漏记由既有日对账偏差告警兜底，漏记比错记可检出）
		log.Printf("[swapledger] triple swap day unknown for %s/%s, skip leg (fail-closed, no Wednesday fallback)", venueKey, canonical)
		return 0
	}
	if day.Weekday() == tripleDay {
		return 3
	}
	return 1
}

// decimalAmount 经 decimalutil 精度守卫计算 swap/funding 金额（D-077 修复⑩ G5）。
// 公式：rate / divRate / divPeriod × notional × multiplier
// 经 strconv 字符串中介避免 float64 漂移，返回 float64 供 InsertCashflow。
func decimalAmount(rate float64, divRate, divPeriod int, notional float64, multiplier float64) float64 {
	dRate, _ := decimalutil.FromFloat64(rate)
	dNotional, _ := decimalutil.FromFloat64(notional)
	dMult, _ := decimalutil.FromFloat64(multiplier)
	result := dRate.Mul(dNotional).Mul(dMult)
	if divRate > 0 {
		result = result.Div(decimalutil.MustFromFloat64(float64(divRate)))
	}
	if divPeriod > 0 {
		result = result.Div(decimalutil.MustFromFloat64(float64(divPeriod)))
	}
	f, _ := result.Float64()
	return f
}
