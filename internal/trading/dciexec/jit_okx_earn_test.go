// jit_okx_earn_test.go —— 返修⑤锚测：留备式 sweep 双态。
package dciexec

import (
	"context"
	"fmt"
	"testing"
)

func TestJitOkxEarn_ReserveSweepStates(t *testing.T) {
	// 态①：free>reserve+floor → 归集触发域（excess>minSweep）。
	if ex := sweepExcess(2000, jitOkxReserveDefaultUSD); ex < jitIdleMinSweepUSD {
		t.Fatalf("态①应触发归集 excess=%v", ex)
	}
	// 态②：free<reserve+floor → 停（excess≤0 如实静默）。
	if ex := sweepExcess(400, jitOkxReserveDefaultUSD); ex >= jitIdleMinSweepUSD {
		t.Fatalf("态②应停归集 excess=%v", ex)
	}
}

// 动态留备锚测：JitOkxArmedMarginUSD 供值→reserve=dynamic；读败→static 兜底。
func TestJitOkxReserve_Dynamic(t *testing.T) {
	ex := &Executor{Store: &fakeStore{armedMarginUSD: 770}}
	r, src := ex.jitOkxReserve(context.Background())
	if src != "dynamic" || r != 770 {
		t.Fatalf("动态留备应取 armed 汇总值, got %.2f src=%s", r, src)
	}
}
func TestJitOkxReserve_Fallback(t *testing.T) {
	ex := &Executor{Store: &fakeStore{armedMarginErr: fmt.Errorf("db down")}}
	r, src := ex.jitOkxReserve(context.Background())
	if src != "static-fallback" || r != jitOkxReserveDefaultUSD {
		t.Fatalf("读败应回静态兜底, got %.2f src=%s", r, src)
	}
}
