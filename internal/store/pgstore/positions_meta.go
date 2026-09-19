// positions_meta.go D-341 S1：meta 更新族（自 positions.go 拆出保行预算；含 D-341 学习样本锚两列）。
package pgstore

import "context"

// UpdatePositionMeta 回填持仓元数据（D-087 §3.4 typed 化）。
// 调用方按型构造 PositionMeta，本方法按字段写入（typed 列分离，不再混用 expected_net_bps）。
func (s *Store) UpdatePositionMeta(ctx context.Context, id int64, posType string, m PositionMeta) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE positions SET type = $1, expected_net_bps = $2, expected_entry_spread_bps = $3,
			expected_swap_bps_daily = $4, expected_annual_pct = $5,
			entry_noise_bps = $6, entry_harvest_floor_bps = $7 WHERE id = $8`,
		posType, m.NetBps, m.EntrySpreadBps, m.SwapBpsDaily, m.AnnualPct, m.EntryNoiseBps, m.EntryHarvestFloorBps, id)
	return err
}

// UpdatePositionOppID 更新持仓的 opportunity_id（D-070 swap 套利用）。
func (s *Store) UpdatePositionOppID(ctx context.Context, id int64, oppID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE positions SET opportunity_id = $1 WHERE id = $2`, oppID, id)
	return err
}
