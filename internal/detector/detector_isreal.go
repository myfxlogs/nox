package detector

// isRealPair D-178：检测层 is_real 配对检查（fail-open）。
// realMap nil/空 或 venue 缺失 → 放行（未知不拦截，gate 层 fail-closed 兜底）。
// 两腿 is_real 都已知且不相等 → 拒绝（demo×real 虚假配对）。
func isRealPair(realMap map[string]*bool, venueA, venueB string) bool {
	if len(realMap) == 0 {
		return true
	}
	a, okA := realMap[venueA]
	b, okB := realMap[venueB]
	if !okA || !okB || a == nil || b == nil {
		return true
	}
	return *a == *b
}
