package autotrade

import "arb/internal/data/isreal"

// 三态：实盘/demo/未知 同类型配对安全红线。
// 首个实盘账户入池触发——组合引擎不区分 demo/实盘，随时可能开出
// 「实盘多×demo 空」的假对冲仓（demo 腿盈亏非真钱，基差风险全在真金侧）。
// discoverSwapCombos 硬过滤：候选组合两腿 is_real 必须相等且非 nil（未知不配对）。

// swapComboCandidate 自动扫描发现的 swap 套利候选组合（basisBps 只展示不动排序，D-140 S3）。
type swapComboCandidate struct {
	canonical, vLong, vShort string
	netSwapBps, basisBps     float64
	basisAvailable           bool   // D-169 P0：quotes nil 或腿缺失→false（trySwapArb fail-closed）
	swapSrc                  string // D-180 S2：settled|spec|none（决策日志带 swap_src）
	strategy                 string // D-187：SWAPARB 或 CARRY（netSwap 换算年化 ≥ CarryMinNetAnnualized → CARRY）
}

// isRealPair D-144/D-190：发现层 is_real 配对检查（fail-closed）。
// 语义：未知不配对（保守：未标注的账户不参与组合）；两腿已知且相等 → 配对。
// 实现迁移至 isreal.Pair（单一真相源，failOpen=false）。
func isRealPair(realMap map[string]*bool, venueA, venueB string) bool {
	return isreal.Pair(realMap, venueA, venueB, false)
}
