// paper_cex_skips.go —— D-051③：纸面入口拦截原因计数器（wire 单实例共享给
// StepPaperCexCarry 与 selfwatch 日报分布；进程内累计，日报侧 app_config
// 快照差分得周期分布——重启清零差分回落当前值，诚实不臆造）。
package scan

import "sync"

// PaperSkipCounter 跳过原因→累计次数（mutex 守护；engine 帧 goroutine 与
// selfwatch 读侧并发安全）。
type PaperSkipCounter struct {
	mu sync.Mutex
	m  map[string]int
}

// Add 计数+1（nil 安全——opts.Skips 未注入时无开销）。
func (c *PaperSkipCounter) Add(reason string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]int{}
	}
	c.m[reason]++
}

// Snapshot 当前累计副本（selfwatch 差分基线输入）。
func (c *PaperSkipCounter) Snapshot() map[string]int {
	out := map[string]int{}
	if c == nil {
		return out
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.m {
		out[k] = v
	}
	return out
}
