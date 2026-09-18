package swapledger

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"arb/internal/decimalutil"
	"arb/internal/store/pgstore"

	"github.com/shopspring/decimal"
)

// factKey (canonical, venue) → swap/funding rate。
type factKey struct {
	canonical, venue string
}

// FactKey D-193-A 扩包 S3：导出版 factKey 供探针 cmd 构造 map 用（与内部 factKey 同构）。
type FactKey = factKey

// NewFactKey D-193-A 扩包 S3：构造 FactKey（canonical=fact.Symbol，venue=fact.Venue）。
// 供探针 cmd 从 fact.Fact 构造 map key 用。
func NewFactKey(canonical, venue string) FactKey {
	return factKey{canonical: canonical, venue: venue}
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

// sellCanonical 解析 SELL 腿侧向 canonical（D-330 断层④修复；镜像 autotrade
// recalcXAssetNetCarry 出口链 xassetSellCanonical 三段语义，尾段回落 canonicalFromOppID
// =向后兼容现状——双腿同 canonical 对解析结果不变）：
// ①opportunities.canonical_sell（opp 行命中且非空）→ ②卖腿 broker_symbol_map
// （bare venue+Symbol→Canonical）→ ③canonicalFromOppID。
// ②查询 DB 失败 → err（调用方该腿 skip 记 reason，不整仓 fail）；③不可空（oppID
// canonical 已由 accountPosition 前置校验）。
// XASSET 数据模型 canonical=买侧、canonical_sell=卖侧：CLUSDT 型持仓 SELL 腿
// canonical CLUSDT→WTI 是修正（WTI 才是 USOILm canonical，facts 按 canonical 存）。
func (l *Ledger) sellCanonical(ctx context.Context, oppID string, legs []pgstore.PositionLeg) (string, error) {
	if opp, err := l.store.GetOpportunity(ctx, oppID); err == nil &&
		opp != nil && opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		return *opp.CanonicalSell, nil
	}
	for i := range legs {
		if legs[i].Phase == "OPEN" && legs[i].Side != "BUY" {
			maps, err := l.store.ListSymbolMappings(ctx, venueFromKey(legs[i].Venue))
			if err != nil {
				return "", fmt.Errorf("list symbol mappings: %w", err)
			}
			for _, m := range maps {
				if m.BrokerSymbol == legs[i].Symbol && m.Canonical != "" {
					return m.Canonical, nil
				}
			}
			break // 卖腿映射无行 → 兜底 ③（解不出=现状，§2.3 向后兼容）
		}
	}
	return canonicalFromOppID(oppID), nil
}

// cashflowKind 按持仓类型+腿 venue 返回 cashflow kind（swap 或 funding）。
// XASSET_CARRY per-leg 判定：CEX perp 腿=funding，MT 腿=swap（D-314）。
func cashflowKind(posType, venue string) string {
	if isFundingLeg(posType, venue) {
		return "funding"
	}
	return "swap"
}

// isFundingLeg 判定单腿是否走 funding 路径（D-314 per-leg 扩展）。
// XASSET_CARRY: CEX perp 腿=funding，MT 腿=swap；其他类型按 posType（向后兼容）。
func isFundingLeg(posType, venue string) bool {
	if posType == "XASSET_CARRY" {
		return strings.HasSuffix(venue, ":perp")
	}
	return isFundingType(posType)
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

// settleMultiplier 返回当日结算乘数（D-077 V2→修复⑪A M3+M4→D-110 周末不开盘修复）。
// 优先级：symbol 级 Fact > venue 级 Fact > fail-closed（跳过+warn，禁止回退已知错误值）。
// 三倍日→3 否则 1。TripleDayResolver 未知（ok=false）→ 返回 0（调用方跳过该腿记账）。
// D-110：tripleDay=Sunday/Saturday 时返回 1（外汇市场周末不开盘，三倍日不生效）。
func settleMultiplier(venueKey, canonical string, day time.Time, r TripleDayResolver) float64 {
	venueName := venueFromKey(venueKey)
	tripleDay, ok := r.TripleSwapDay(venueName, canonical)
	if !ok {
		// D-077 修复⑪A M4：删周三回退 → fail-closed（漏记由既有日对账偏差告警兜底，漏记比错记可检出）
		log.Printf("[swapledger] triple swap day unknown for %s/%s, skip leg (fail-closed, no Wednesday fallback)", venueKey, canonical)
		return 0
	}
	// D-110：周末不开盘，三倍日=Sunday/Saturday 时不收3倍（D-102 实证：IC 结算逐日 1x/周末 0）
	if tripleDay == time.Sunday || tripleDay == time.Saturday {
		return 1
	}
	if day.Weekday() == tripleDay {
		return 3
	}
	return 1
}

// decimalAmount 经 decimalutil 精度守卫计算 swap/funding 金额（D-077 修复⑩ G5）。
// 公式：rate / divRate / divPeriod × notional × multiplier
// 经 strconv 字符串中介避免 float64 漂移，返回 float64 供 InsertCashflow。
// D-190 §3.3：NaN/Inf 输入 → return 0 + log（金额守卫：不 panic、不静默传 NaN）；
// divRate/divPeriod 为 int → decimal.NewFromInt 精确构造（删 MustFromFloat64 panic 路径）。
func decimalAmount(rate float64, divRate, divPeriod int, notional float64, multiplier float64) float64 {
	if math.IsNaN(rate) || math.IsInf(rate, 0) || math.IsNaN(notional) || math.IsInf(notional, 0) || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) {
		log.Printf("[swapledger] decimalAmount NaN/Inf guard: rate=%v notional=%v mult=%v", rate, notional, multiplier)
		return 0
	}
	dRate, _ := decimalutil.FromFloat64(rate)
	dNotional, _ := decimalutil.FromFloat64(notional)
	dMult, _ := decimalutil.FromFloat64(multiplier)
	result := dRate.Mul(dNotional).Mul(dMult)
	if divRate > 0 {
		result = result.Div(decimal.NewFromInt(int64(divRate)))
	}
	if divPeriod > 0 {
		result = result.Div(decimal.NewFromInt(int64(divPeriod)))
	}
	f, _ := result.Float64()
	return f
}
