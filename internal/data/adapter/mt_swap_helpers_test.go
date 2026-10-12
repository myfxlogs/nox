package adapter

import (
	"testing"

	"nox/internal/data/fact"
)

// TestApplyTripleSwapDayOverride D-115：实测 venue 覆盖、未实测 venue 原样返回。
func TestApplyTripleSwapDayOverride(t *testing.T) {
	// 实测 venue → 全符号替换为 Wednesday(3)
	in := map[string]float64{"AUDJPY": 0, "EURUSD": 0} // API 返回 Sunday(0)
	got := applyTripleSwapDayOverride("REDACTED_MT_ACCT", in)
	if got["AUDJPY"] != 3 || got["EURUSD"] != 3 {
		t.Errorf("REDACTED_MT_ACCT override: want Wednesday(3) for all, got AUDJPY=%v EURUSD=%v", got["AUDJPY"], got["EURUSD"])
	}

	// 未实测 venue → 原样返回
	got = applyTripleSwapDayOverride("52993526", in)
	if got["AUDJPY"] != 0 || got["EURUSD"] != 0 {
		t.Errorf("52993526 no override: want original Sunday(0), got AUDJPY=%v EURUSD=%v", got["AUDJPY"], got["EURUSD"])
	}
}

// TestCollectTripleSwapDayFacts_OverrideApplied D-115：override venue 写入 Wednesday(3)。
func TestCollectTripleSwapDayFacts_OverrideApplied(t *testing.T) {
	// REDACTED_MT_ACCT API 返回 Sunday(0)，override 后应写 Wednesday(3)
	facts := collectTripleSwapDayFacts("mt5", "REDACTED_MT_ACCT", map[string]float64{"AUDJPY": 0, "EURUSD": 0})
	if len(facts) != 1 {
		t.Fatalf("want 1 venue-level fact, got %d", len(facts))
	}
	if facts[0].Kind != fact.KindTripleSwapDay {
		t.Errorf("kind want %s, got %s", fact.KindTripleSwapDay, facts[0].Kind)
	}
	if facts[0].Value != 3 {
		t.Errorf("override value want 3 (Wednesday), got %v", facts[0].Value)
	}
}

// TestCollectTripleSwapDayFacts_NoOverride D-115：未实测 venue 保持 API 原值。
func TestCollectTripleSwapDayFacts_NoOverride(t *testing.T) {
	// 52993526 不在 override map → 保持 Sunday(0)
	facts := collectTripleSwapDayFacts("mt5", "52993526", map[string]float64{"AUDJPY": 0})
	if len(facts) != 1 {
		t.Fatalf("want 1 fact, got %d", len(facts))
	}
	if facts[0].Value != 0 {
		t.Errorf("no override value want 0 (Sunday), got %v", facts[0].Value)
	}
}
