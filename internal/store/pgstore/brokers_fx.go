// broker_accounts 存储（docs/broker管理器实现方案.md §1）。
package pgstore

import (
	"context"
	"log"
	"nox/internal/data/keydomain"
	"nox/internal/platform/notional"
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
func (s *Store) USDPer(ctx context.Context, quoteCcy string, at time.Time, preferVenueKey string) (float64, bool) {
	if quoteCcy == "USD" {
		return 1.0, true
	}
	pair, invert, ok := notional.FXPair(quoteCcy)
	if !ok {
		return 0, false
	}
	// D-633 B 档：venue 键域优先（preferVenueKey 剥 bare——跨 broker 脏锚修正）；
	// miss 退 symbol-only 旧窗+WARN（D-634 显式例外域）。
	bare := keydomain.BareOf(preferVenueKey)
	if bare != "" {
		var rate float64
		err := s.pool.QueryRow(ctx, `
			SELECT bid FROM ticks
			WHERE symbol = $1 AND venue = $2 AND ts <= $3 AND ts > $3 - interval '24 hours'
			ORDER BY ts DESC LIMIT 1
		`, pair, bare, at).Scan(&rate)
		if err == nil && rate > 0 {
			if invert {
				return 1.0 / rate, true
			}
			return rate, true
		}
		log.Printf("[fx] USDPer prefer %s@%s miss→symbol-only 退窗（D-634 例外）", pair, bare)
	}
	var rate float64
	err := s.pool.QueryRow(ctx, `
		SELECT bid FROM ticks
		WHERE symbol = $1 AND ts <= $2 AND ts > $2 - interval '24 hours'
		ORDER BY ts DESC LIMIT 1
	`, pair, at).Scan(&rate)
	if err != nil || rate <= 0 {
		return 0, false
	}
	if invert {
		return 1.0 / rate, true
	}
	return rate, true
}
