// broker_accounts 存储（docs/broker管理器实现方案.md §1）。
package pgstore

import (
	"context"
	"time"
)

// USDPer 返回计价币→USD 的汇率（D-087 §3.1 FXRateProvider，返修包②七直盘全覆盖）。
//   - "USD" → (1.0, true) 基准
//   - USD-base 直盘（JPY/CHF/CAD）→ 1 / USD{ccy} bid（USDJPY/USDCHF/USDCAD）
//   - USD-quote 直盘（EUR/GBP/AUD/NZD）→ {ccy}USD bid（EURUSD/GBPUSD/AUDUSD/NZDUSD）
//   - ""/其他 → (0, false) 诚实缺省（未知=换算不可得，调用方记 skipped 不猜）
//
// 查询窗口：ticks 表 symbol=对应直盘、ts ≤ at 最近一条（24h 窗口）；miss → false 诚实跳过。
// 依据：七直盘全部在采（ticks 实证 EURUSD/GBPUSD/AUDUSD/NZDUSD/USDJPY/USDCHF/USDCAD 各 6.3k~12.7k 行）。
func (s *Store) USDPer(ctx context.Context, quoteCcy string, at time.Time) (float64, bool) {
	switch quoteCcy {
	case "USD":
		return 1.0, true
	case "JPY", "CHF", "CAD":
		// USD-base 直盘：USD per ccy = USD{ccy} bid（如 USDJPY=150 → 1 JPY = 1/150 USD）
		var rate float64
		err := s.pool.QueryRow(ctx, `
			SELECT bid FROM ticks
			WHERE symbol = $1 AND ts <= $2 AND ts > $2 - interval '24 hours'
			ORDER BY ts DESC LIMIT 1
		`, "USD"+quoteCcy, at).Scan(&rate)
		if err != nil || rate <= 0 {
			return 0, false
		}
		return 1.0 / rate, true
	case "EUR", "GBP", "AUD", "NZD":
		// USD-quote 直盘：USD per ccy = {ccy}USD bid（如 EURUSD=1.08 → 1 EUR = 1.08 USD）
		var rate float64
		err := s.pool.QueryRow(ctx, `
			SELECT bid FROM ticks
			WHERE symbol = $1 AND ts <= $2 AND ts > $2 - interval '24 hours'
			ORDER BY ts DESC LIMIT 1
		`, quoteCcy+"USD", at).Scan(&rate)
		if err != nil || rate <= 0 {
			return 0, false
		}
		return rate, true
	default:
		return 0, false
	}
}
