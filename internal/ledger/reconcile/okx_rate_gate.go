// okx_rate_gate.go —— okx REST 节流闸（429 修）薄壳：本体迁 platform/okxrate
// （共额罚窗版——balance/earn/dci 多面共额收敛票）。语义：同端点最小时隙
// +任一面 429→全局罚窗全面退避（venue 限频档=User ID 级共享预算）。
// 本文件保留只为 ConfigureOkxRateGate 装配口兼容（main.go 一行不动）。
package reconcile

import (
	"time"

	"nox/internal/platform/okxrate"
)

// ErrOkxThrottled 兼容别名（既有调方 errors.Is 判定不变）。
var ErrOkxThrottled = okxrate.ErrThrottled

// ConfigureOkxRateGate boot overlay 注入参数（main.go 装配口不变）。
func ConfigureOkxRateGate(min, backoff, backoffMax time.Duration) {
	okxrate.Configure(min, backoff, backoffMax)
}
