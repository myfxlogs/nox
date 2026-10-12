// Package combo 组合逻辑键单点实现（缺陷3修复：D-073+，D-080 组合键贯通）。
// 四处自拼字符串（机会卡 oppKey / gate DedupKey / positions.opportunity_id 前缀 / 弹窗跳转匹配串）
// 统一引用本包，任一处格式变化即断链的风险消除。
//
// 格式 = 6 段组合键：strategy:canonical:venueBuy:venueType:venueSell:venueType
// venue 含 ":mt5"/":mt4" 后缀整体为一段（不拆分）。
// 旧 tick 格式 = 组合键 + ":" + 13 位时间戳（7 段）；新持仓直接写组合键（6 段）。
package combo

import "strings"

// Key 组合逻辑键 = strategy:canonical:venueBuy:venueSell。
// gate 构造 DedupKey / 对账关联 / S4 匹配全部改引用它，删各处自拼。
func Key(strategy, canonical, venueBuy, venueSell string) string {
	return strategy + ":" + canonical + ":" + venueBuy + ":" + venueSell
}

// FromOppID 从 opportunity_id 派生 ComboKey。
// 仅当尾段为纯数字（旧 tick 时间戳）时剥离——旧 ID→组合键，新 ID（尾段=venueType
// 如 mt5/perp，非纯数字）原样返回。天然版本兼容，无需格式标记。
func FromOppID(oppID string) string {
	idx := strings.LastIndex(oppID, ":")
	if idx < 0 {
		return oppID // 无 ":" 异常输入，原样返回（不 panic）
	}
	tail := oppID[idx+1:]
	if !isAllDigits(tail) {
		return oppID // 尾段非纯数字（新组合键格式）→ 原样返回
	}
	return oppID[:idx]
}

// MatchOppID 判断 opportunity_id 是否属于给定 ComboKey。
// 补等值命中（新持仓 oppID==组合键时必须算命中）；
// 旧 tick 格式走前缀+":" 边界匹配。
func MatchOppID(oppID, comboKey string) bool {
	if oppID == comboKey {
		return true // 新格式：oppID 即组合键
	}
	if len(oppID) <= len(comboKey) {
		return false
	}
	return oppID[:len(comboKey)] == comboKey && oppID[len(comboKey)] == ':'
}

// isAllDigits 判断字符串是否全为 ASCII 数字（空串→false）。
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
