// xasset_coverage.go —— D-625 P1 数据覆盖账本（纯读面）：每 (pair,cex_venue,
// mt_venue) 簿覆盖指标——24h 行数/最新行龄/noise 非空率。回放窗准入与
// 「数据不可判」告警族同读一口。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// XAssetCoverageRow 单三元组覆盖报告。
type XAssetCoverageRow struct {
	Pair       string
	CexVenue   string
	MtVenue    string
	Rows24h    int
	LastTs     time.Time
	FirstTs    time.Time // 簿首行（全表 min(ts)——新键爬坡窗判据）
	NoiseNulls int       // 24h 内 noise_bps NULL 行数（衍生轴缺计）
}

// ListXAssetCoverage 全三元组覆盖报表（24h 窗 GROUP BY；零行=簿全缺席——
// 调用面按「无证据不误赦」自选判）。
func (s *Store) ListXAssetCoverage(ctx context.Context) ([]XAssetCoverageRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT pair, cex_venue, mt_venue,
		       count(*) FILTER (WHERE ts > now()-interval '24 hours') AS rows_24h,
		       max(ts) AS last_ts,
		       min(ts) AS first_ts,
		       count(*) FILTER (WHERE ts > now()-interval '24 hours' AND noise_bps IS NULL) AS noise_nulls
		FROM fin_xasset_monitor
		GROUP BY 1,2,3 ORDER BY 1,2,3`)
	if err != nil {
		return nil, fmt.Errorf("xasset coverage: %w", err)
	}
	defer rows.Close()
	var out []XAssetCoverageRow
	for rows.Next() {
		var r XAssetCoverageRow
		if err := rows.Scan(&r.Pair, &r.CexVenue, &r.MtVenue, &r.Rows24h, &r.LastTs, &r.FirstTs, &r.NoiseNulls); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CoverageWindowOK 回放窗可用性列（P3 回测准入闸消费面）：
// 指定三元组在 [from,to] 窗内行数≥minRows 且最新行龄≤maxStale=true。
func (s *Store) CoverageWindowOK(ctx context.Context, pair, cexVenue, mtVenue string, from, to time.Time, minRows int, maxStale time.Duration) (bool, error) {
	var n int
	var last *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT count(*), max(ts) FROM fin_xasset_monitor
		WHERE pair=$1 AND cex_venue=$2 AND mt_venue=$3 AND ts>=$4 AND ts<=$5`,
		pair, cexVenue, mtVenue, from, to).Scan(&n, &last)
	if err != nil {
		return false, fmt.Errorf("coverage window: %w", err)
	}
	if n < minRows || last == nil {
		return false, nil
	}
	return to.Sub(*last) <= maxStale, nil
}
