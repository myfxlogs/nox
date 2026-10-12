// Package verification 机会验证阶梯（V-Ladder · D-086）纯函数。
//
// 业主定性：未经验证的预估是假设不是机会。本包提供验证阶梯的纯计算核心：
//   - ExpectedUsd：按型折算期望收益 USD（CROSS_VENUE 一次性 bps / FUNDING 族年化%×天数/365 / SWAPARB 日 bps×天数）
//   - VerifyLevel：V0/V1 阶梯判定（V2 真金级预留，本包不实现判定）
//   - Vkey / VkeyFromOppID：验证键派生（"{type}:{venue_buy}:{venue_sell}"）
package verification

import (
	"arb/internal/data/keydomain"
	"math"
	"strings"
)

// 验证阶梯级别。
const (
	LevelV0 = 0 // 模型级：过全部口径门槛，灰显不推送
	LevelV1 = 1 // 对账级：R=Σrealized/Σexpected≥minRatio 且样本够且新鲜，亮灯可推送
	// V2 真金级预留，本包不实现判定（需真金账户标记，后置真金阶段）
)

// ExpectedUsd 按型折算期望收益 USD（D-087 §3.4 typed 新签名）。
// 各型按字段语义取值，nil/≤0 视为 0：
//   - CROSS_VENUE/MEANREVERT：netBps/10000 × notionalUSD（一次性 bps）
//   - FUNDING_HEDGE/PERP_FUNDING/CARRY：netBps/100 × notionalUSD × holdDays/365（年化百分点）
//   - SWAPARB：swapBpsDaily/10000 × notionalUSD × holdDays（日 bps，entrySpread 不参与期望收益）
//
// notionalUSD=Σ OPEN FILLED 腿 qty×CS×open价×fx；holdDays=（closed_at−开仓时刻）/24h。
// 未知类型返回 0（不纳入统计）。
func ExpectedUsd(typ string, netBps, swapBpsDaily, notionalUSD, holdDays float64) float64 {
	if notionalUSD <= 0 {
		return 0
	}
	switch typ {
	case "CROSS_VENUE", "MEANREVERT":
		return netBps / 10000 * notionalUSD
	case "FUNDING_HEDGE", "PERP_FUNDING", "CARRY", "XASSET_CARRY": // D-331：XASSET net_bps=年化百分点同口径（pos 488 实证）
		if holdDays <= 0 || netBps <= 0 {
			return 0
		}
		return netBps / 100 * notionalUSD * holdDays / 365
	case "SWAPARB":
		if holdDays <= 0 || swapBpsDaily <= 0 {
			return 0
		}
		return swapBpsDaily / 10000 * notionalUSD * holdDays
	default:
		return 0
	}
}

// IsCarryType 判断是否为息差型策略（D-087 §3.4 收敛——SWAPARB/CARRY/FUNDING_HEDGE/PERP_FUNDING 统一；D-300 +XASSET_CARRY）。
// 息差型期望收益随持有天数增长；收敛型（CROSS_VENUE/MEANREVERT）为一次性 bps。
func IsCarryType(typ string) bool {
	switch typ {
	case "SWAPARB", "CARRY", "FUNDING_HEDGE", "PERP_FUNDING", "XASSET_CARRY":
		return true
	default:
		return false
	}
}

// VerifyLevel 验证阶梯判定（纯函数，不含真金标记判定）。
//   - fresh && samples >= minSamples && ratio >= minRatio → V1
//   - 否则 → V0
//
// ratio 为 NaN（expected_usd≤0 时分母为 0）→ V0。
// minRatio≤0 或 minSamples≤0 → 门槛关闭，仅凭 fresh 判 V1（测试用）。
func VerifyLevel(samples, minSamples int, ratio, minRatio float64, fresh bool) int {
	if !fresh {
		return LevelV0
	}
	if math.IsNaN(ratio) {
		return LevelV0
	}
	if minSamples > 0 && samples < minSamples {
		return LevelV0
	}
	if minRatio > 0 && ratio < minRatio {
		return LevelV0
	}
	return LevelV1
}

// Vkey 构造验证键 "{type}:{venue_buy}:{venue_sell}"。
// venue 含 ":spot"/":perp"/":mt5" 段（与 opportunities 列同构）。
func Vkey(typ, venueBuy, venueSell string) string {
	return typ + ":" + venueBuy + ":" + venueSell
}

// VkeyFromOppID 从 opportunity_id（组合键格式 strategy:canonical:venueBuy:venueSell）
// 派生验证键。venueBuy/venueSell 各含一个冒号（如 "binance:spot"），故按 6 段拆分。
// 旧 tick 格式（7 段尾时间戳）先截尾再拆。异常输入返回空串。
func VkeyFromOppID(oppID string) string {
	ck := comboKeyFromOppID(oppID)
	parts := strings.Split(ck, ":")
	// 6 段 = strategy:canonical:venue:venueType:venue:venueType
	if len(parts) < 6 {
		return ""
	}
	typ := parts[0]
	venueBuy := keydomain.Join(parts[2], parts[3])
	venueSell := keydomain.Join(parts[4], parts[5])
	return Vkey(typ, venueBuy, venueSell)
}

// comboKeyFromOppID 剥离旧 tick 格式尾段时间戳（纯数字），返回组合键。
// 与 combo.FromOppID 等价但本包不依赖 combo 包（避免环依赖）。
func comboKeyFromOppID(oppID string) string {
	idx := strings.LastIndex(oppID, ":")
	if idx < 0 {
		return oppID
	}
	tail := oppID[idx+1:]
	if !isAllDigits(tail) {
		return oppID // 新格式（尾段=venueType 非纯数字）→ 原样
	}
	return oppID[:idx]
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
