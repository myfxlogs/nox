package autotrade

// D-144 ① 三态：实盘/demo/未知 同类型配对安全红线。
// 首个实盘账户入池触发——组合引擎不区分 demo/实盘，随时可能开出
// 「实盘多×demo 空」的假对冲仓（demo 腿盈亏非真钱，基差风险全在真金侧）。
// discoverSwapCombos 硬过滤：候选组合两腿 is_real 必须相等且非 nil（未知不配对）。

// swapComboCandidate 自动扫描发现的 swap 套利候选组合（basisBps 只展示不动排序，D-140 S3）。
type swapComboCandidate struct {
	canonical, vLong, vShort string
	netSwapBps, basisBps     float64
	basisAvailable           bool   // D-169 P0：quotes nil 或腿缺失→false（trySwapArb fail-closed）
	swapSrc                  string // D-180 S2：settled|spec|none（决策日志带 swap_src）
}

// isRealPair D-144 ① 三态：两腿 venue 必须同类型且均非未知——杜绝实盘×demo 假对冲 + 未知不配对。
// realMap 中 venue 缺失或 is_real=nil（未知）→ 不配对（保守：未标注的账户不参与组合）。
// 两腿均 true 或均 false → 配对；任一 nil 或缺失 → 不配对。
func isRealPair(realMap map[string]*bool, venueA, venueB string) bool {
	a, okA := realMap[venueA]
	b, okB := realMap[venueB]
	if !okA || !okB || a == nil || b == nil {
		return false // 缺失或未知 → 不配对
	}
	return *a == *b
}
