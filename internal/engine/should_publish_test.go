package engine

import (
	"testing"
)

// TestOppTopicPublishAlways D-086 实施修正①防回归守卫：
// oppTopic 是双用途数据总线（autotrade rollup 喂入 + 前端实时流），永不论证级别过滤。
// 无论 store 有无样本、R 高低、新鲜与否——入库即发布。
// 装配同 notify_test（newNotifyEngine + carryCand + scanCandidate）。
func TestOppTopicPublishAlways(t *testing.T) {
	cand := carryCand(20.0) // gross=20, margin=4 → net=16 > 0 入库
	e, ch, _ := newNotifyEngine(t, cand)
	scanCandidate(t, e, cand)

	// 入库 1 条 → 必发布 1 条（无论验证状态，总线不过滤）
	got := drainTopic(ch)
	if got != 1 {
		t.Errorf("oppTopic 发布 %d 条，期望 1（总线永不过滤，入库即发布）", got)
	}
}
