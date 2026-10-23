// jit_okx_earn_test.go —— 返修⑤锚测：留备式 sweep 双态。
package dciexec

import "testing"

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
