// entry_streak.go D-383 Phase C：paper spot_carry 进场 N 轮持续闸计数器。
// 进程内存态（wire_scan 装配一次、逐轮传入 StepPaperSpotCarry），重启清零=保守向，
// 不需新表（设计 §6.2）；与既有 ≥48h 存续年龄闸（spotCarryMinAge）正交。
package scan

import "sync"

// PaperEntryStreakRounds 进场持续闸轮数：候选须连续 N 轮满足全部开仓条件才放行
// （退出侧已有 paperExitStreakRounds=2 连续判定，本闸补齐进场对称性，设计公理 3）。
const PaperEntryStreakRounds = 3

// EntryStreak 候选键（venue|symbol，与 StepPaperSpotCarry 内 activeByKey 同键）的
// 连续达标轮数；多 venue 同名 symbol 因键含 venue 天然隔离。
type EntryStreak struct {
	mu     sync.Mutex
	rounds map[string]int
}

// NewEntryStreak 创建全零计数器（每进程一次，wire_scan 装配位创建）。
func NewEntryStreak() *EntryStreak {
	return &EntryStreak{rounds: make(map[string]int)}
}

// Step 记一轮：passed=本轮满足全部开仓条件的候选键；既有键不在 passed 即清零
// （轮内任一条件失败、候选从 uni 消失同义）；返回已连续满 n 轮的键集。
func (es *EntryStreak) Step(passed []string, n int) map[string]struct{} {
	es.mu.Lock()
	defer es.mu.Unlock()
	passedSet := make(map[string]struct{}, len(passed))
	for _, k := range passed {
		passedSet[k] = struct{}{}
	}
	for k := range es.rounds {
		if _, ok := passedSet[k]; !ok {
			delete(es.rounds, k)
		}
	}
	eligible := make(map[string]struct{})
	for _, k := range passed {
		es.rounds[k]++
		if es.rounds[k] >= n {
			eligible[k] = struct{}{}
		}
	}
	return eligible
}

// Drop 开仓成功后删键（已 active，次轮起不再入候选）。
func (es *EntryStreak) Drop(key string) {
	es.mu.Lock()
	defer es.mu.Unlock()
	delete(es.rounds, key)
}
