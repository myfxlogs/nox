package pgstore

import (
	"context"
	"fmt"
)

// venueOpenPositionPred 活跃真金仓状态域（与 combo_daily.go open_positions 口径一致）。
const venueOpenPositionPred = `'OPENING','OPEN','LEGGED','CLOSING'`

// CountVenueOpenPositions D-150 删除闸：venue 上仍有活跃真金仓 → RemoveBroker 拒绝。
// 挂空仓删号=孤儿仓位+账本断链，fail-closed。
func (s *Store) CountVenueOpenPositions(ctx context.Context, venue string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM position_legs pl JOIN positions p ON pl.position_id = p.id
		 WHERE pl.venue = $1 AND p.status IN (`+venueOpenPositionPred+`)`, venue).Scan(&n)
	return n, err
}

// PurgeVenueData D-150：账号删除级联清市场观测域（业主令：删号即清采集数据）。
// 覆盖 ticks/facts/asset_equiv/fin_carry_universe/fin_xasset_monitor（typed+bare 键形）+
// active 纸面仓退出 venue_removed（不留孤儿行）。
// 保留域：alerts/positions/position_legs 等审计域一行不动。
// 返回各表清理行数（观测日志用）。
func (s *Store) PurgeVenueData(ctx context.Context, venue string) (map[string]int64, error) {
	out := map[string]int64{}
	del := func(name, q string, args ...any) error {
		tag, err := s.pool.Exec(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("purge %s: %w", name, err)
		}
		out[name] = tag.RowsAffected()
		return nil
	}
	// 纸面仓先退出（UPDATE 不是 DELETE——行留作退出史，仅状态翻）。
	tag, err := s.pool.Exec(ctx,
		`UPDATE paper_spot_carry SET status='exited', exit_reason='venue_removed', exit_ts=now()
		 WHERE status='active' AND (mt_venue=$1 OR venue_a=$1 OR venue_b=$1)`, venue)
	if err != nil {
		return out, fmt.Errorf("purge paper positions: %w", err)
	}
	out["paper_exited"] = tag.RowsAffected()
	for _, d := range []struct{ name, q string }{
		{"ticks", `DELETE FROM ticks WHERE venue=$1`},
		{"facts", `DELETE FROM facts WHERE venue=$1`},
		{"asset_equiv", `DELETE FROM asset_equiv WHERE venue=$1`},
		{"fin_carry_universe", `DELETE FROM fin_carry_universe WHERE mt_venue=$1`},
		{"fin_xasset_monitor", `DELETE FROM fin_xasset_monitor WHERE mt_venue=$1 OR mt_venue LIKE $1 || ':%'`},
	} {
		if err := del(d.name, d.q, venue); err != nil {
			return out, err
		}
	}
	return out, nil
}
