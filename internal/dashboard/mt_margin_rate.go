// mt_margin_rate.go —— D-006 S1b 消费端杠杆语义：effLev=min(缓存杠杆,20)，
// 无缓存/陈旧/无限杠杆哨兵 → 按 20 处理（悲观默认=最坏假设，天然 fail-closed）。
// fact 值口径=init_margin_rate=1/lev → 率下限=1/20（即所需保证金率不小于 5%）。
package dashboard

import (
	"time"

	"arb/internal/fact"
)

// effMTMarginRateFloor D-006 悲观档下界：init_margin_rate 不得小于 1/20。
const effMTMarginRateFloor = 1.0 / 20.0

// effMTMarginRateMaxAge 消费端新鲜窗。D-068：与 ARB_MT_HEALTH_MARGIN_CRIT 缺省
// 同值——init_margin_rate 为准静态、重连驱动写入（D-067），监控判 crit 之前
// 消费端不得先判陈；两处若改须同改。
const effMTMarginRateMaxAge = 72 * time.Hour

// effMTMarginRate 消费 init_margin_rate fact（≤72h 新鲜）：返回 (率, 是否实测)。
// 实测值低于下限（高杠杆/无限杠杆哨兵 0）→ 抬到下限（effLev≤20 等效）；
// 缺行/超窗/读败 → (1/20, false)——调用方按悲观默认用数，注记未实测。
func effMTMarginRate(fs []fact.Fact, err error, now time.Time) (rate float64, measured bool) {
	if err == nil && len(fs) > 0 && now.Sub(fs[0].Ts) <= effMTMarginRateMaxAge {
		if fs[0].Value > effMTMarginRateFloor {
			return fs[0].Value, true
		}
		return effMTMarginRateFloor, true // 含无限杠杆哨兵 0：实测但抬到下限
	}
	return effMTMarginRateFloor, false
}
