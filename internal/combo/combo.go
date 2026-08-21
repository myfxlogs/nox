// Package combo 组合逻辑键单点实现（缺陷3修复：D-073+）。
// 四处自拼字符串（机会卡 oppKey / gate DedupKey / positions.opportunity_id 前缀 / 弹窗跳转匹配串）
// 统一引用本包，任一处格式变化即断链的风险消除。
//
// 格式 = 既有 DedupKey：strategy:canonical:venueBuy:venueSell
// venue 含 ":mt5"/":mt4" 后缀整体为一段（不拆分）。
// positions.opportunity_id = ComboKey + ":" + 时间戳；截尾 helper 互逆。
package combo

import "strings"

// Key 组合逻辑键 = strategy:canonical:venueBuy:venueSell。
// gate 构造 DedupKey / 对账关联 / S4 匹配全部改引用它，删各处自拼。
func Key(strategy, canonical, venueBuy, venueSell string) string {
	return strategy + ":" + canonical + ":" + venueBuy + ":" + venueSell
}

// FromOppID 从 opportunity_id 截尾派生 ComboKey（去掉最后一段时间戳）。
// opportunity_id = ComboKey + ":" + 时间戳；venue 含 ":" 后缀不影响 LastIndex 截尾。
// 禁止裸 strings.Split 散落，匹配一律经此 helper。
func FromOppID(oppID string) string {
	idx := strings.LastIndex(oppID, ":")
	if idx < 0 {
		return oppID // 无 ":" 异常输入，原样返回（不 panic）
	}
	return oppID[:idx]
}

// MatchOppID 判断 opportunity_id 是否属于给定 ComboKey（前缀匹配 + ":" 边界）。
// 比 FromOppID 截尾后 == 比较更直接，避免截尾 helper 在匹配场景的二次分配。
func MatchOppID(oppID, comboKey string) bool {
	if len(oppID) <= len(comboKey) {
		return false
	}
	return oppID[:len(comboKey)] == comboKey && oppID[len(comboKey)] == ':'
}
