// jit_okx_earn_test.go —— 返修③锚测：armed 缺资时归集不触发（设计锚测#3）。
package dciexec

import (
	"context"
	"testing"
)

func TestJitOkxEarn_ArmedShortfallBlocks(t *testing.T) {
	e := &Executor{Store: nil} // rail 缺席仍会先过 armed 闸（闸先于 rail 读）
	ArmedShortfallFn = func(_ context.Context, _ string) (float64, error) { return 500, nil }
	defer func() { ArmedShortfallFn = nil }()
	e.JitOkxEarnEvent(context.Background()) // 缺资→本拍停归集如实静默（无 panic=闸生效）
	ArmedShortfallFn = func(_ context.Context, _ string) (float64, error) { return 0, context.Canceled }
	e.JitOkxEarnEvent(context.Background()) // 读败亦 fail-closed 停归集
	// 反态（返修④防过杀）：armed 非缺资（0）→ 闸放行进入 rail 判定（rail 缺席
	// 走到 earnRail 报缺席=闸未哑停断言——判败含 "earn rail 缺席" 即闸过）。
	ArmedShortfallFn = func(_ context.Context, _ string) (float64, error) { return 0, nil }
	if err := e.jitOkxEarnSweep(context.Background()); err == nil || err.Error() != "okx earn rail 缺席" {
		t.Fatalf("armed 非缺资态闸应放行进入 rail 判定，err=%v", err)
	}
}
