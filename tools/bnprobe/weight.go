// weight.go —— 棘轮拆出件（main.go 超 300 行分流）：权重自限闸族。
package main

import (
	"fmt"
	"net/http"
)

// usedWeight 读 USED-WEIGHT 头族实值（418 自限票——预节流须 venue 实报非猜）：
// 新头族 X-MBX-USED-WEIGHT-1M 优先（venue 分钟窗口径），legacy X-MBX-USED-WEIGHT
// 兜底，两族皆现取大者（保守计——预算共享面宁多算）。
func usedWeight(h http.Header) string {
	best := 0
	for _, k := range []string{"X-MBX-USED-WEIGHT-1M", "X-MBX-USED-WEIGHT"} {
		var w int
		if fmt.Sscanf(h.Get(k), "%d", &w); w > best {
			best = w
		}
	}
	if best == 0 {
		return ""
	}
	return fmt.Sprintf("%d", best)
}

// weightGate 权重自限闸（418 自限票——撞墙前停）：used>cap*pct/100 即截批。
// 阈值单源=CLI flag（-weightcap/-weightpct——禁写死多处）。
func weightGate(w, cap, pct int) bool {
	if cap <= 0 || w <= 0 {
		return false // 无阈/无头读=不截（闸无据不动——头缺面照旧 pace+418 底闸）
	}
	return w > cap*pct/100
}
