// facts_asof.go —— as-of-t 时态读面（D-625 P3 回测引擎前置抽象）：ts≤t 最新值
// 查询。LatestFactsByKind 非时态（now 域最新）不可直用于回放——回放任意历史拍
// t 的「当时最新」必须由本层供给。DISTINCT ON (venue,symbol) 单遍组最新。
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"nox/internal/data/fact"
)

// LatestFactsByKindAsOf kind 全组 as-of 快照：每 (venue,symbol) 取 ts≤asOf 最新行。
// 语义与 LatestFactsByKind 一致（时点参数化）——回放拍 t 喂判定链的合法读口。
func (s *Store) LatestFactsByKindAsOf(ctx context.Context, kind string, asOf time.Time) ([]fact.Fact, error) {
	rows, err := s.pool.Query(ctx, `
		WITH grp AS (
			SELECT DISTINCT ON (venue, symbol) kind, venue, symbol, value, unit, ts, src
			FROM facts
			WHERE kind = $1 AND symbol IS NOT NULL AND ts <= $2
			ORDER BY venue, symbol, ts DESC
		)
		SELECT kind, venue, symbol, value, unit, ts, src FROM grp
		ORDER BY venue, symbol`, kind, asOf)
	if err != nil {
		return nil, fmt.Errorf("facts as-of %s@%s: %w", kind, asOf, err)
	}
	defer rows.Close()
	var out []fact.Fact
	for rows.Next() {
		var f fact.Fact
		if err := rows.Scan(&f.Kind, &f.Venue, &f.Symbol, &f.Value, &f.Unit, &f.Ts, &f.Src); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// LatestTickAsOf 单 (venue,symbol) as-of 最新 tick（回放步进报价锚）。
// ok=false=asOf 前零行（如实缺——回放面禁拿现在值顶替历史拍）。
func (s *Store) LatestTickAsOf(ctx context.Context, venue, symbol string, asOf time.Time) (bid, ask float64, ts time.Time, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT bid, ask, ts FROM ticks
		WHERE venue = $1 AND symbol = $2 AND ts <= $3
		ORDER BY ts DESC LIMIT 1`, venue, symbol, asOf).Scan(&bid, &ask, &ts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, time.Time{}, false, nil
		}
		return 0, 0, time.Time{}, false, fmt.Errorf("tick as-of %s|%s@%s: %w", venue, symbol, asOf, err)
	}
	return bid, ask, ts, true, nil
}
