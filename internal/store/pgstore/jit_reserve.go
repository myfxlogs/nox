package pgstore

import (
	"context"
	"fmt"
)

// JitOkxArmedMarginUSD okx 腿 armed 组合保证金需汇总（动态留备口径）：
// margin_usd 直值优先 → notional_usd/lever → 组合默认额（NULL 档位保守折）。
// lever/comboDefault 由调用方从 appConfig 读入（读败有默认件）；
// 行缺/全 NULL 组合按 comboDefault 计（pct sizing 型组合无静态名义额）。
func (s *Store) JitOkxArmedMarginUSD(ctx context.Context, lever, comboDefault float64) (float64, error) {
	var v float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE
			WHEN margin_usd IS NOT NULL AND margin_usd > 0 THEN margin_usd
			WHEN notional_usd IS NOT NULL AND notional_usd > 0 THEN notional_usd / $1
			ELSE $2 END), 0)
		FROM xasset_allow_combos
		WHERE enabled AND tier='armed'
		  AND (venue_key_buy LIKE 'okx%' OR venue_key_sell LIKE 'okx%')`, lever, comboDefault).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("jit okx armed margin: %w", err)
	}
	return v, nil
}
