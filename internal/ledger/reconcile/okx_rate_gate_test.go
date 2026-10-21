// okx_rate_gate_test.go —— 兼容壳锚：ErrOkxThrottled 别名与 Configure 委派
// 保真（行为件全量锚见 platform/okxrate/gate_test.go——本包只钉接口面）。
package reconcile

import (
	"testing"

	"nox/internal/platform/okxrate"
)

func TestOkxRateGateCompat(t *testing.T) {
	if ErrOkxThrottled != okxrate.ErrThrottled {
		t.Fatal("别名失配——调方 errors.Is(ErrOkxThrottled) 判定会断")
	}
	// Configure 委派不 panic（参数直通 okxrate.Configure）。
	ConfigureOkxRateGate(0, 0, 0)
}
