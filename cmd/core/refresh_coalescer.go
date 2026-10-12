// refresh_coalescer.go 热修 hotfix-paper：前端刷新扇出合流（OOM pprof 实证——
// 纸面开平 Publish("paper_carry")→前端重取 ListPaperSpotCarry，帧率级调用扇出
// RPC 风暴：5 万 exited 行×protojson+deflate=GB 级分配）。key 级 2s 尾沿合流：
// 窗内重复调用只留一个补发定时器（终态不丢），频率上界 0.5/s。刷新信号非判定流。
package main

import (
	"sync"
	"time"

	"nox/internal/data/bus"
)

// refreshCoalesceWindow 合流窗（var：测试覆写）。
var refreshCoalesceWindow = 2 * time.Second

// refreshCoalescer 按 key 合流 topic 发布：同 key 窗内多次调用只发一次。
type refreshCoalescer struct {
	topic *bus.Topic[string]
	mu    sync.Mutex
	pend  map[string]*time.Timer
}

func newRefreshCoalescer(topic *bus.Topic[string]) *refreshCoalescer {
	return &refreshCoalescer{topic: topic, pend: make(map[string]*time.Timer)}
}

// Publish 窗内首调挂 2s 尾沿补发定时器；窗内重复调用合流丢弃（终态由补发携带）。
func (c *refreshCoalescer) Publish(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.pend[key]; ok {
		return
	}
	c.pend[key] = time.AfterFunc(refreshCoalesceWindow, func() {
		c.topic.Publish(key)
		c.mu.Lock()
		delete(c.pend, key)
		c.mu.Unlock()
	})
}
