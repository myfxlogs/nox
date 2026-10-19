// venue_onboard.go —— 新户入册放行/闸行缺口：在册 venue_key 集读面
// （exchange_configs + broker_accounts 两源 union——venue_key=venue:type 域）。
// 消费=autotrade ReloadAllowRules 轴周期对拍放行单/敞口闸行，缺行→
// permission 型 condition_gap（放行/闸行缺口机制化落箱，非人工作业）。
package pgstore

import (
	"context"
	"fmt"
)

// ListOnboardVenueKeys 在册 venue_key 全集（两源 union 去重；倒序无依赖）。
func (s *Store) ListOnboardVenueKeys(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT v FROM (
			SELECT venue || ':' || venue_type AS v FROM exchange_configs
			UNION
			SELECT venue || ':' || venue_type AS v FROM broker_accounts) t ORDER BY v`)
	if err != nil {
		return nil, fmt.Errorf("onboard venue keys: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
