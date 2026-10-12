// loop.go 调度壳（D-513 §1 sysretro 同构）：next=min(daily 06:47 BJ, weekly 周日 07:13 BJ)
// 单次定时器循环；触发即跑对应扫描；运行行落 log/alert。同日重跑幂等。
package explorer

import (
	"context"
	"log"
	"time"
)

// Run 探矿员主循环（startExplorer goroutine 调用；ctx 取消即退）。
// tickHook 仅供测试断言触发（nil=正常路径）。
func Run(ctx context.Context, deps Deps, tickHook func(kind string)) {
	for {
		now := deps.Now
		if now == nil {
			now = time.Now
		}
		d := NextDelay(now(), "daily")
		w := NextDelay(now(), "weekly")
		kind := "daily"
		wait := d
		if w < d {
			kind, wait = "weekly", w
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if tickHook != nil {
			tickHook(kind)
		}
		if deps.ResolveClient != nil {
			c, err := deps.ResolveClient(ctx)
			if err != nil {
				log.Printf("[explorer] resolve client: %v", err)
				continue
			}
			deps.Client = c
		}
		switch kind {
		case "weekly":
			res, err := RunWeekly(ctx, deps)
			if err != nil {
				log.Printf("[explorer] weekly err: %v", err)
				deps.alert(ctx, "warn", "[探索] 周报生成失败："+err.Error())
			} else {
				log.Printf("[explorer] weekly ok: theme=%s path=%s", res.Theme, res.Path)
			}
		default:
			res, err := RunDaily(ctx, deps)
			if err != nil {
				log.Printf("[explorer] daily err: %v", err)
				deps.alert(ctx, "warn", "[探索] 日报生成失败："+err.Error())
			} else {
				log.Printf("[explorer] daily ok: proposals=%d drop=%d", res.Proposals, res.Drop)
			}
		}
	}
}
