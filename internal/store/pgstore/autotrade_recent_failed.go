package pgstore

import (
	"context"
	"fmt"
	"time"
)

// ListRecentFailedAutotradeLogs D-560 S2b：近 window FAILED 行批量面（拒事实透出用，
// 禁 N+1）——venue_buy/sell 按 venue_type 列拼回复合键；按 ts DESC 返回，
// 调用方 errclass.IsFundingRejectMsg 过滤（判据单源不在 SQL 复制）。
func (s *Store) ListRecentFailedAutotradeLogs(ctx context.Context, window time.Duration) ([]AutotradeLog, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT canonical,
			venue_buy || CASE WHEN venue_type_buy <> '' THEN ':' || venue_type_buy ELSE '' END,
			venue_sell || CASE WHEN venue_type_sell <> '' THEN ':' || venue_type_sell ELSE '' END,
			reason, ts
		FROM autotrade_logs
		WHERE status = 'FAILED' AND ts > now() - make_interval(secs => $1)
		ORDER BY ts DESC`, window.Seconds())
	if err != nil {
		return nil, fmt.Errorf("recent failed autotrade logs: %w", err)
	}
	defer rows.Close()
	var out []AutotradeLog
	for rows.Next() {
		var l AutotradeLog
		if err := rows.Scan(&l.Canonical, &l.VenueBuy, &l.VenueSell, &l.Reason, &l.Ts); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
