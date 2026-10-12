package detector

import "nox/internal/data/isreal"

// isRealPair D-178/D-190：检测层 is_real 配对检查（fail-open）。
// 语义：未知放行（gate 层 fail-closed 兜底）；两腿已知且不相等 → 拒绝。
// 实现迁移至 isreal.Pair（单一真相源，failOpen=true）。
func isRealPair(realMap map[string]*bool, venueA, venueB string) bool {
	return isreal.Pair(realMap, venueA, venueB, true)
}
