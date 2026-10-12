package engine

import "testing"

// TestDisabledStrategies_SuppressesOpportunity 验证 §1 自动淘汰闭环：
// strategy_stats.disabled=true 的策略产出 → 不入库不发布（系统已自证亏损，展示假机会误导）。
func TestDisabledStrategies_SuppressesOpportunity(t *testing.T) {
	cand := carryCand(20.0) // CARRY, net 16
	e, ch, store := newNotifyEngine(t, cand)
	e.SetDisabledStrategies(map[string]bool{"CARRY": true})
	scanCandidate(t, e, cand)
	if n := drainTopic(ch); n != 0 {
		t.Errorf("disabled strategy publish count want 0, got %d", n)
	}
	if c := store.count(); c != 0 {
		t.Errorf("disabled strategy upsert count want 0, got %d", c)
	}
}

// TestDisabledStrategies_NonDisabledPasses 未 disabled 的策略 → 正常入库发布。
func TestDisabledStrategies_NonDisabledPasses(t *testing.T) {
	cand := carryCand(20.0) // CARRY, net 16
	e, ch, store := newNotifyEngine(t, cand)
	e.SetDisabledStrategies(map[string]bool{"PERP_FUNDING": true, "CROSS_VENUE": true}) // CARRY 未禁用
	scanCandidate(t, e, cand)
	if n := drainTopic(ch); n != 1 {
		t.Errorf("non-disabled publish count want 1, got %d", n)
	}
	if c := store.count(); c != 1 {
		t.Errorf("non-disabled upsert count want 1, got %d", c)
	}
}

// TestDisabledStrategies_EmptySetPasses 空/nil disabled 集合 → 全部通过（测试默认行为不变）。
func TestDisabledStrategies_EmptySetPasses(t *testing.T) {
	e, ch, store := newNotifyEngine(t, carryCand(20.0))
	// 不调用 SetDisabledStrategies（nil）→ 不排除。
	scanCandidate(t, e, carryCand(20.0))
	if n := drainTopic(ch); n != 1 {
		t.Errorf("nil disabled set publish count want 1, got %d", n)
	}
	if c := store.count(); c != 1 {
		t.Errorf("nil disabled set upsert count want 1, got %d", c)
	}
}

// TestDisabledStrategies_RuntimeUpdate 验证运行时刷新：先放行 → SetDisabledStrategies 禁用 → 再扫描被拦。
func TestDisabledStrategies_RuntimeUpdate(t *testing.T) {
	cand := carryCand(20.0)
	e, ch, store := newNotifyEngine(t, cand)
	// 第一次：无 disabled → 入库发布
	scanCandidate(t, e, cand)
	if n := drainTopic(ch); n != 1 {
		t.Fatalf("first scan publish count want 1, got %d", n)
	}
	if c := store.count(); c != 1 {
		t.Fatalf("first scan upsert count want 1, got %d", c)
	}
	// 运行时禁用 CARRY
	e.SetDisabledStrategies(map[string]bool{"CARRY": true})
	// 第二次：同 id 刷新但 CARRY 已 disabled → 不入库不发布
	scanCandidate(t, e, cand)
	if n := drainTopic(ch); n != 0 {
		t.Errorf("after disable publish count want 0, got %d", n)
	}
	if c := store.count(); c != 1 { // 仍 1（不新增，旧记录 TTL 自然过期）
		t.Errorf("after disable upsert count want 1（no new）, got %d", c)
	}
}
