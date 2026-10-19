// xasset_regime.go —— D-625 P2：regime 三指标读面（窗口 spread 统计供标签化）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// XAssetSpreadStats 三元组窗内 spread 统计（regime 标签原料）。
type XAssetSpreadStats struct {
	Rows   int
	Stdev  float64
	Mean   float64
	LastTs time.Time
	Delta  float64 // 最新-最早 entry_spread（趋势代理）
}

// XAssetSpreadStats 读窗内 spread 统计（行数<2→Stdev/Delta=0 如实缺）。
func (s *Store) XAssetSpreadStats(ctx context.Context, pair, cexVenue, mtVenue string, since time.Time) (XAssetSpreadStats, error) {
	var r XAssetSpreadStats
	err := s.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(stddev_samp(entry_spread_bps),0), COALESCE(avg(entry_spread_bps),0),
		       max(ts),
		       (array_agg(entry_spread_bps ORDER BY ts DESC))[1]-(array_agg(entry_spread_bps ORDER BY ts ASC))[1]
		FROM fin_xasset_monitor
		WHERE pair=$1 AND cex_venue=$2 AND mt_venue=$3 AND ts>=$4`,
		pair, cexVenue, mtVenue, since).Scan(&r.Rows, &r.Stdev, &r.Mean, &r.LastTs, &r.Delta)
	if err != nil {
		return r, fmt.Errorf("xasset spread stats: %w", err)
	}
	return r, nil
}
