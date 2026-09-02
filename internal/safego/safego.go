// Package safego 提供带 recover 的 goroutine 启动包装（D-190 修复 C）。
//
// 修复前：全仓 goroutine 启动点无 recover，任一 panic 致进程崩溃。
// 修复后：统一 safego.Go 包装，panic → log + stack，进程存活。
//
// 依赖仅标准库 log/runtime/debug（叶子包，零内部依赖）。
package safego

import (
	"log"
	"runtime/debug"
)

// Go 启动带 recover 的 goroutine。panic → log [safego:name] + debug.Stack()，进程存活。
// name 用于日志定位（建议格式 `文件短名:语义`，如 `main:bpMon.Run`）。
//
// 已知限制（勿擅自扩）：不自动重启——恢复即该 goroutine 终止，
// 报价类停摆由 mt-health/staleness 告警兜底。
func Go(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[safego:%s] panic recovered: %v\n%s", name, r, debug.Stack())
			}
		}()
		fn()
	}()
}
