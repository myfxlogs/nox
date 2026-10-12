// max_positions_decision_test.go —— maxPositionsReachedDecision 锚（gate_test 拆出，D-212 拆搬）。
package autotrade

import "testing"

// --- H4.1 maxPositionsReachedDecision 单测（满仓短路纯函数） ---

func TestMaxPositionsReachedDecision_Full(t *testing.T) {
	if !maxPositionsReachedDecision(5, 5) {
		t.Error("openCount==maxPositions should be reached (short-circuit)")
	}
	if !maxPositionsReachedDecision(6, 5) {
		t.Error("openCount>maxPositions should be reached (short-circuit)")
	}
}

func TestMaxPositionsReachedDecision_NotFull(t *testing.T) {
	if maxPositionsReachedDecision(4, 5) {
		t.Error("openCount<maxPositions should NOT be reached")
	}
	if maxPositionsReachedDecision(0, 5) {
		t.Error("openCount=0 should NOT be reached")
	}
}
