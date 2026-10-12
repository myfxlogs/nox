// Package safego 提供带 recover 的 goroutine 启动包装（D-190 修复 C）。
//
// 修复前：全仓 goroutine 启动点无 recover，任一 panic 致进程崩溃。
// 修复后：统一 safego.Go 包装，panic → log + stack，进程存活。
// panic 增设 OnPanic 全局告警钩（cmd/core 装配 alerts critical 去重
// 落库——静默失联整类消除）；常驻循环用 GoLoop 获 panic→退避重启自愈
// （D-113 事故：positionLoop panic 后协程消亡，退出评估停摆 37min 零报警）。
//
// 依赖仅标准库 log/runtime/debug/time（叶子包，零内部依赖）。
package safego

import (
	"log"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// OnPanic 全局 panic 钩子（cmd/core 装配→alerts critical 落库去重；nil=仅 log）。
// willRestart=true 表示该协程将自动重启（GoLoop 域），false=协程就此终止（Go 域）。
// 钩子自身 panic 被吞，不连坐监控面。atomic 装载——panic 路径与装配写并发安全。
var OnPanic atomic.Pointer[func(name string, recovered any, willRestart bool)]

// SetOnPanic 装配钩子（boot 期单次调用；重复调用后值覆盖）。
func SetOnPanic(fn func(name string, recovered any, willRestart bool)) {
	if fn == nil {
		OnPanic.Store(nil)
		return
	}
	f := fn
	OnPanic.Store(&f)
}

// OnBeat/OnExit 心跳与退出钩（D-190 员工花名册：beat=协程节拍心跳、
// exit=fn 退出事件；装配侧接 PG upsert；nil=仅无操作）。atomic 同 OnPanic 域。
var OnBeat atomic.Pointer[func(name string)]
var OnExit atomic.Pointer[func(name string)]

// SetOnBeat/SetOnExit 装配钩子（boot 期单次调用）。
func SetOnBeat(fn func(name string)) { storeHook(&OnBeat, fn) }
func SetOnExit(fn func(name string)) { storeHook(&OnExit, fn) }

func storeHook(slot *atomic.Pointer[func(string)], fn func(string)) {
	if fn == nil {
		slot.Store(nil)
		return
	}
	f := fn
	slot.Store(&f)
}

// Beat 心跳：Go/GoLoop 在 fn 启动时自动打一次；长驻内循环可显式 Beat(name)
// 挂自身节拍刷新（零新 goroutine——D-190 语义）。
func Beat(name string) { fireHook(&OnBeat, name) }

func fireHook(slot *atomic.Pointer[func(string)], name string) {
	fn := slot.Load()
	if fn == nil {
		return
	}
	defer func() { _ = recover() }()
	(*fn)(name)
}

func fireOnPanic(name string, r any, willRestart bool) {
	fn := OnPanic.Load()
	if fn == nil {
		return
	}
	defer func() { _ = recover() }()
	(*fn)(name, r, willRestart)
}

// Go 启动带 recover 的 goroutine。panic → log [safego:name] + debug.Stack() +
// OnPanic 告警钩，进程存活；恢复即该 goroutine 终止（任务级/一次性/retry spawn
// 保持本形态——重启语义不安全者禁转 GoLoop）。
func Go(name string, fn func()) {
	go func() {
		Beat(name)
		defer fireHook(&OnExit, name)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[safego:%s] panic recovered: %v\n%s", name, r, debug.Stack())
				fireOnPanic(name, r, false)
			}
		}()
		fn()
	}()
}

// GoLoop 常驻循环协程：panic → log+stack+OnPanic(willRestart=true) → 退避重启
// （1s 起步×2 封顶 30s；距上次 panic>60s 视为新 episode 重置退避）。
// fn 正常返回（ctx 取消语义）即终止，不重启。仅用于 Run 型守护循环——
// 任务级/执行路径/逐事件 spawn 用 Go（重启=重复副作用风险）。
func GoLoop(name string, fn func()) {
	go func() {
		backoff := time.Second
		lastPanic := time.Time{}
		for {
			var stack []byte
			Beat(name)
			panicked := func() (r any) {
				defer func() {
					if p := recover(); p != nil {
						r = p
						stack = debug.Stack()
					}
				}()
				fn()
				return nil
			}()
			if panicked == nil {
				fireHook(&OnExit, name)
				return
			}
			log.Printf("[safego:%s] panic recovered, restarting: %v\n%s", name, panicked, stack)
			fireOnPanic(name, panicked, true)
			if time.Since(lastPanic) > time.Minute {
				backoff = time.Second
			}
			lastPanic = time.Now()
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}()
}
