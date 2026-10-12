// 位点③：Gate funding 结算周期数据驱动推导（SSOT §3 v2）。
// 根因：exchange_history.go Gate 历史路径 annualizeFunding 硬编码 ×3（8h），
// Gate 4h 品种历史年化低估 2×（D-302 同病；binance=缓存 D-301a、okx=探测 D-321 均已在位）。
// 修法：相邻结算行时间差的众数 → 每日次数（缺行缺口=间隔的整数倍，众数天然鲁棒）；
// 样本行 <3 或众数不唯一 → 回退 3 + warn（D-079 禁静默）。
package exchange

import "fmt"

// gatePeriodDeriver per-symbol 滚动推导器（跨页/跨轮持久 lastT，由 GateFundingHistory 持有）。
type gatePeriodDeriver struct {
	lastT   float64       // 上一观测行 t（Unix 秒）
	hasLast bool          // 首行无 Δt
	rows    int           // 样本行数（<3 回退——行数而非 Δt 数：3 行 4h 序列即出 2 个 Δ）
	deltas  map[int64]int // |Δt| 秒 → 出现次数
}

func newGatePeriodDeriver() *gatePeriodDeriver {
	return &gatePeriodDeriver{deltas: map[int64]int{}}
}

// observe 喂一行结算时刻（升序/降序序列皆可——取正差）。
func (d *gatePeriodDeriver) observe(tSec float64) {
	if d.hasLast {
		delta := int64(tSec - d.lastT)
		if delta < 0 {
			delta = -delta
		}
		if delta > 0 {
			d.deltas[delta]++
		}
	}
	d.lastT, d.hasLast, d.rows = tSec, true, d.rows+1
}

// periodsPerDay Δt 众数 → 每日次数（86400/众数）。样本不足/众数不唯一/非法值 → 回退 3 + warn。
func (d *gatePeriodDeriver) periodsPerDay() int {
	if d.rows < 3 {
		return fundingHistoryPeriodsPerDay
	}
	mode, count := int64(0), 0
	tie := false
	for dt, n := range d.deltas {
		if n > count {
			mode, count, tie = dt, n, false
		} else if n == count {
			tie = true
		}
	}
	if count == 0 || (tie && len(d.deltas) > 1) || mode <= 0 || 86400%mode != 0 {
		fmt.Printf("[exchange:gate] funding_history period derive: no mode (deltas=%v rows=%d), fallback 3/day\n", d.deltas, d.rows)
		return fundingHistoryPeriodsPerDay
	}
	return int(86400 / mode)
}
