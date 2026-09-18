// D-335 S2：XASSET 入口安全边际闸读接口（monitor 单源：entry+noise 同行读）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// LatestXAssetEntryGate 读该 pair 最新行的 entry_spread_bps+noise_bps。
// 无行、行龄>maxAge、noise_bps IS NULL（样本<12 未成噪）→ ok=false（调用方 fail-open）。
// pair 键以 monitor 工具构造为准（现值 "XAUUSDT-XAU"）。
func (s *Store) LatestXAssetEntryGate(ctx context.Context, pair string, maxAge time.Duration) (entryBps, noiseBps float64, ok bool, err error) {
	var entry, noise *float64
	var ts time.Time
	err = s.pool.QueryRow(ctx, `
		SELECT entry_spread_bps, noise_bps, ts
		FROM fin_xasset_monitor
		WHERE pair = $1
		ORDER BY ts DESC
		LIMIT 1`, pair).Scan(&entry, &noise, &ts)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return 0, 0, false, nil
		}
		return 0, 0, false, fmt.Errorf("latest xasset entry gate: %w", err)
	}
	if entry == nil || noise == nil || time.Since(ts) > maxAge {
		return 0, 0, false, nil
	}
	return *entry, *noise, true, nil
}
