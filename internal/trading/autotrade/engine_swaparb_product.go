package autotrade

// sameProductGroup D-173：产品维度配对校验纯函数。
// 任一腿 product_group 未知（空）→ true（向后兼容放行）；
// 两腿都已知且相同 → true；两腿都已知但不同 → false。
func sameProductGroup(longPG, shortPG string) bool {
	if longPG == "" || shortPG == "" {
		return true
	}
	return longPG == shortPG
}
