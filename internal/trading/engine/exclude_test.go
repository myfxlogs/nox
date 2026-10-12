package engine

import "testing"

// TestExcludeCanonicals_DetectorLayerFilter 验证检测层排除（D-072 资金提醒弹窗提示词 B1）：
// 命中 excludeCanonicals 的候选 → 不入库不发布（从源头消除 BRENT/WTI 假机会）。
// 复用 ARB_AUTOTRADE_EXCLUDE_CANONICALS（执行层+检测层双用途，P3 单一真相）。
func TestExcludeCanonicals_DetectorLayerFilter(t *testing.T) {
	// BRENT 候选：配置排除后应被过滤。
	cand := carryCand(20.0)
	cand.Canonical = "BRENT"
	e, ch, store := newNotifyEngine(t, cand)
	e.SetExcludeCanonicals(map[string]bool{"BRENT": true, "WTI": true})
	scanCandidate(t, e, cand)
	if n := drainTopic(ch); n != 0 {
		t.Errorf("excluded canonical publish count want 0, got %d", n)
	}
	if c := store.count(); c != 0 {
		t.Errorf("excluded canonical upsert count want 0, got %d", c)
	}
}

// TestExcludeCanonicals_NonExcludedPasses 未命中排除集合 → 正常入库发布。
func TestExcludeCanonicals_NonExcludedPasses(t *testing.T) {
	e, ch, store := newNotifyEngine(t, carryCand(20.0)) // AAA, net 16
	e.SetExcludeCanonicals(map[string]bool{"BRENT": true, "WTI": true})
	scanCandidate(t, e, carryCand(20.0))
	if n := drainTopic(ch); n != 1 {
		t.Errorf("non-excluded publish count want 1, got %d", n)
	}
	if c := store.count(); c != 1 {
		t.Errorf("non-excluded upsert count want 1, got %d", c)
	}
}

// TestExcludeCanonicals_EmptySetPasses 空/nil 排除集合 → 全部通过（测试默认行为不变）。
func TestExcludeCanonicals_EmptySetPasses(t *testing.T) {
	e, ch, store := newNotifyEngine(t, carryCand(20.0))
	// 不调用 SetExcludeCanonicals（nil）→ 不排除。
	scanCandidate(t, e, carryCand(20.0))
	if n := drainTopic(ch); n != 1 {
		t.Errorf("nil exclude set publish count want 1, got %d", n)
	}
	if c := store.count(); c != 1 {
		t.Errorf("nil exclude set upsert count want 1, got %d", c)
	}
}
