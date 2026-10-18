// facts_suffix.go —— 后缀形 facts 读件（facts.go ≤300 行棘轮门禁拆出）：
// 判定径「该 symbol 有无行」专用变体——trade_session 等分段键族（symbol#N）
// 裸名精确匹配必空的键形错位修。
package pgstore

import (
	"context"
	"fmt"
	"time"

	"nox/internal/data/fact"
)

// FactsSinceSymbolOrSuffix FactsSince 变体——symbol 精确匹配 OR 裸名+`#n` 后缀族
// （trade_session facts 键=symbol#N 按会话分段写——「该 symbol 有无行」判定径专用，
// 读面改 shape 全册消费点零动）。
func (s *Store) FactsSinceSymbolOrSuffix(ctx context.Context, kind, venue, symbol string, since time.Time) ([]fact.Fact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT kind, venue, COALESCE(symbol,'') AS symbol, value, unit, ts, src
		FROM facts
		WHERE kind = $1 AND (venue_key = $2 OR venue = $2) AND (symbol = $3 OR symbol LIKE $3 || '#%%') AND ts >= $4
		ORDER BY ts`, kind, venue, symbol, since)
	if err != nil {
		return nil, fmt.Errorf("facts since (suffix): %w", err)
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
