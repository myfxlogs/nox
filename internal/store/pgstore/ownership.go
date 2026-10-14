// ownership.go —— D-516 共享持有归属系数：app_config `ownership_share:<venue>:
// <venue_type>:<ccy>`=归属系数（0<share≤1，业主域值可改）。系数在池读径边界
// 单源乘入（CapitalPoolSnapshots/ExplorerIdleFunds 同挂）——NAV/敞口分母/
// 浓度/资金地图自动只认归属份；余量存 Custodial 字段供面板标「代管」，
// 不进任何判据。缺键=1.0 全归属（行为零变）。读败=fail-closed 系数全 1 降级？
// 否——读败返 error 让调用方按各自语义（读面 fail-closed 不静默错价）。
package pgstore

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// OwnershipShareKey app_config 键形：ownership_share:<venue>:<venue_type>:<ccy>。
const OwnershipSharePrefix = "ownership_share:"

// OwnershipShares 归属系数表：key=venue+"\x00"+venueType+"\x00"+ccy → share。
// 非法键段/越界值（≤0 或 >1）跳过不计（配置坏不放大资产）。
func (s *Store) OwnershipShares(ctx context.Context) (map[string]float64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT key, value FROM app_config WHERE key LIKE 'ownership_share:%'`)
	if err != nil {
		return nil, fmt.Errorf("ownership shares: %w", err)
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		parts := strings.Split(strings.TrimPrefix(k, OwnershipSharePrefix), ":")
		f, ferr := strconv.ParseFloat(v, 64)
		if len(parts) != 3 || ferr != nil || f <= 0 || f > 1 {
			continue
		}
		out[parts[0]+"\x00"+parts[1]+"\x00"+strings.ToUpper(parts[2])] = f
	}
	return out, rows.Err()
}

// OwnershipShare 池行系数查：命中→(share, true)；缺→(1, false)。
func OwnershipShare(shares map[string]float64, venue, venueType, ccy string) (float64, bool) {
	f, ok := shares[venue+"\x00"+venueType+"\x00"+strings.ToUpper(ccy)]
	return f, ok
}
