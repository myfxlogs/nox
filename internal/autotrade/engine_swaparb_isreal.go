package autotrade

// D-144 ①：实盘/demo 同类型配对安全红线。
// 首个实盘账户入池触发——组合引擎不区分 demo/实盘，随时可能开出
// 「实盘多×demo 空」的假对冲仓（demo 腿盈亏非真钱，基差风险全在真金侧）。
// discoverSwapCombos 硬过滤：候选组合两腿 is_real 必须相等。

// swapComboCandidate 自动扫描发现的 swap 套利候选组合（basisBps 只展示不动排序，D-140 S3）。
type swapComboCandidate struct {
	canonical, vLong, vShort string
	netSwapBps, basisBps     float64
}

// isRealPair D-144 ①：两腿 venue 必须同类型（is_real 相等）——杜绝实盘×demo 假对冲。
// realMap 中 venue 缺失 → 视为 false（默认 demo，存量兼容）。
func isRealPair(realMap map[string]bool, venueA, venueB string) bool {
	return realMap[venueA] == realMap[venueB]
}
