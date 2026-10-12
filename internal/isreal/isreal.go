// Package isreal 提供 is_real 配对检查的单一实现（D-190 修复 B）。
//
// 修复前双实现同名反义：
//   - detector.isRealPair（fail-open：未知放行，gate 层兜底）
//   - autotrade.isRealPair（fail-closed：未知不配对）
//
// 双语义皆正确但同名=认知陷阱。本包用 failOpen 参数显式区分语义。
package isreal

// Pair 单一实现：两腿 is_real 已知且相等 → true；任一缺失/nil → 按 failOpen 决定。
//   - 检测层传 failOpen=true（D-178：未知放行，gate 层 fail-closed 兜底）；
//   - 发现层传 failOpen=false（D-144：未知不配对，保守）。
func Pair(m map[string]*bool, venueA, venueB string, failOpen bool) bool {
	a, okA := m[venueA]
	b, okB := m[venueB]
	if !okA || !okB || a == nil || b == nil {
		return failOpen
	}
	return *a == *b
}
