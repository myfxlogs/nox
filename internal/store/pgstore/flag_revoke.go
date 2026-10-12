// flag_revoke.go D-475a S4：blocked 旗标→xasset_allow_combos 吊销面 +
// 在管仓知情查询。编排层在 dashboard service（revokeCombosForFlag），
// 本文件只含两个原子查询——InsertFlag 保持纯写语义。
package pgstore

import (
	"context"
	"fmt"
)

// DisableCombosByFlag blocked 旗标吊销该 venue（裸名）下受影响组合：
// venue_buy/venue_sell 前缀匹配 'bareVenue:%'（venue_key 前缀）；symbol='*'=
// venue 级吊销全组合，否则仅 canonical_buy/canonical_sell 命中行。
// WHERE enabled 保证幂等可重放 + note 不重复追加。返回停用行数。
func (s *Store) DisableCombosByFlag(ctx context.Context, bareVenue, symbol string) (int64, error) {
	ct, err := s.pool.Exec(ctx, `
		UPDATE xasset_allow_combos
		SET enabled=false, note=note||' [flag吊销:'||$1||']', updated_at=now()
		WHERE enabled
		  AND (venue_buy = $2 OR venue_sell = $2)
		  AND ($3='*' OR canonical_buy=$3 OR canonical_sell=$3)`,
		bareVenue, bareVenue, symbol)
	if err != nil {
		return 0, fmt.Errorf("disable combos by flag %s/%s: %w", bareVenue, symbol, err)
	}
	return ct.RowsAffected(), nil
}

// CountOpenPositionsOnVenue 该 venue（裸名）在管仓计数+posID 列——
// OPEN/LEGGED/CLOSING 三态任一 leg venue 命中即算（吊销知情告警用，不自动平仓）。
func (s *Store) CountOpenPositionsOnVenue(ctx context.Context, bareVenue string) (int64, []int64, error) {
	var n int64
	var ids []int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(DISTINCT p.id), array_agg(DISTINCT p.id)
		FROM positions p JOIN position_legs l ON l.position_id = p.id
		WHERE p.status IN ('OPEN','LEGGED','CLOSING') AND l.venue_key = $1`,
		bareVenue).Scan(&n, &ids)
	if err != nil {
		return 0, nil, fmt.Errorf("count open positions on %s: %w", bareVenue, err)
	}
	return n, ids, nil
}
