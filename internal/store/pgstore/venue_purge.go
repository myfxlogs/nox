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

// BrokerAccountExists D-151 异步清理起步闸：账号若在清理窗内被重建 → 放弃
// 批量清（残行陈旧但随新采集覆盖；继续清会擦掉新账号数据）。
func (s *Store) BrokerAccountExists(ctx context.Context, venue string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM broker_accounts WHERE venue=$1)`, venue).Scan(&ok)
	return ok, err
}

func (s *Store) purgeDel(ctx context.Context, out map[string]int64, name, q string, args ...any) error {
	tag, err := s.pool.Exec(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("purge %s: %w", name, err)
	}
	out[name] = tag.RowsAffected()
	return nil
}

// PurgeVenueDerived D-151 同步半：RemoveBroker 账号行删除前跑——全是小表且喂正确性
// （纸面仓退出 venue_removed + 等价类/universe/监控簿行，不清则幽灵三元组）。
// ticks/facts 批量观测行不在此（PurgeVenueBulk 异步）。
func (s *Store) PurgeVenueDerived(ctx context.Context, venue string) (map[string]int64, error) {
	out := map[string]int64{}
	// 纸面仓先退出（UPDATE 不是 DELETE——行留作退出史，仅状态翻）。
	tag, err := s.pool.Exec(ctx,
		`UPDATE paper_spot_carry SET status='exited', exit_reason='venue_removed', exit_ts=now()
		 WHERE status='active' AND (mt_venue=$1 OR venue_a=$1 OR venue_b=$1)`, venue)
	if err != nil {
		return out, fmt.Errorf("purge paper positions: %w", err)
	}
	out["paper_exited"] = tag.RowsAffected()
	if err := s.purgeDel(ctx, out, "asset_equiv", `DELETE FROM asset_equiv WHERE venue=$1`, venue); err != nil {
		return out, err
	}
	if err := s.purgeVenueDerivedRows(ctx, out, venue); err != nil {
		return out, err
	}
	return out, nil
}

// purgeVenueDerivedRows universe+监控簿删除（同步首扫与异步末扫共用——末扫收扫描竞态行）。
func (s *Store) purgeVenueDerivedRows(ctx context.Context, out map[string]int64, venue string) error {
	if err := s.purgeDel(ctx, out, "fin_carry_universe", `DELETE FROM fin_carry_universe WHERE mt_venue=$1`, venue); err != nil {
		return err
	}
	return s.purgeDel(ctx, out, "fin_xasset_monitor",
		`DELETE FROM fin_xasset_monitor WHERE mt_venue=$1 OR mt_venue LIKE $1 || ':%'`, venue)
}

// PurgeVenueBulk D-151 异步半：RemoveBroker 返回后跑——ticks/facts 批量观测行
// （万行级 DELETE，同步会卡 RPC ~30s）。facts 先删：发现层 venue 过滤对缺席账号
// fail-open，facts 在窗内仍可配对出幽灵 universe 行→最先清最短窗；末尾重扫
// universe/monitor 收该窗内扫描器竞态写入。
func (s *Store) PurgeVenueBulk(ctx context.Context, venue string) (map[string]int64, error) {
	out := map[string]int64{}
	if err := s.purgeDel(ctx, out, "facts", `DELETE FROM facts WHERE venue=$1`, venue); err != nil {
		return out, err
	}
	if err := s.purgeDel(ctx, out, "ticks", `DELETE FROM ticks WHERE venue=$1`, venue); err != nil {
		return out, err
	}
	if err := s.purgeVenueDerivedRows(ctx, out, venue); err != nil {
		return out, err
	}
	return out, nil
}
