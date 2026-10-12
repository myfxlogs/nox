package pgstore

import (
	"context"
	"time"
)

// RadarDigestMember D-392：radar_digest_member 行状态（成员级边沿输入）。
// LastPushedAt NULL=进场未推过；InPrevSet=上一轮 cur 是否含此成员。
type RadarDigestMember struct {
	LastPushedAt *time.Time
	InPrevSet    bool
}

// GetRadarDigestMembers D-392 S2：kind→member→state 全量读出（新入谓词输入）。
func (s *Store) GetRadarDigestMembers(ctx context.Context) (map[string]map[string]RadarDigestMember, error) {
	rows, err := s.pool.Query(ctx, `SELECT kind, member, last_pushed_at, in_prev_set FROM radar_digest_member`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]map[string]RadarDigestMember{}
	for rows.Next() {
		var k, mem string
		var st RadarDigestMember
		if err := rows.Scan(&k, &mem, &st.LastPushedAt, &st.InPrevSet); err != nil {
			return nil, err
		}
		if m[k] == nil {
			m[k] = map[string]RadarDigestMember{}
		}
		m[k][mem] = st
	}
	return m, rows.Err()
}

// ApplyRadarDigestRun D-392 S2：整批推送成功后的单事务记账（失败上抛=本轮状态零更新）。
// cur 必须收全部四类键（空集传空切片）——in_prev 清零不依赖该类本轮有成员
// （空集周期不清零则「离开>24h 再回归」被残留标记永久误压，自审缺陷 2）。
// 每类：存量行 in_prev_set=false → cur 成员 upsert true（last_pushed_at 保留）→
// pushed 成员盖 last_pushed_at=now → 顺手删 ∉cur 且未在册且（NULL 或超 24h）的过期行。
func (s *Store) ApplyRadarDigestRun(ctx context.Context, cur map[string][]string, pushed map[string][]string, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	cutoff := now.Add(-24 * time.Hour) // flap TTL=24h，与 last_alert_ts 冷却同口径
	for kind, members := range cur {
		if _, err := tx.Exec(ctx, `UPDATE radar_digest_member SET in_prev_set=false WHERE kind=$1`, kind); err != nil {
			return err
		}
		for _, m := range members {
			if _, err := tx.Exec(ctx,
				`INSERT INTO radar_digest_member (kind, member, in_prev_set) VALUES ($1,$2,true)
				 ON CONFLICT (kind, member) DO UPDATE SET in_prev_set=true`, kind, m); err != nil {
				return err
			}
		}
		if p := pushed[kind]; len(p) > 0 {
			if _, err := tx.Exec(ctx,
				`UPDATE radar_digest_member SET last_pushed_at=$2 WHERE kind=$1 AND member=ANY($3)`, kind, now, p); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM radar_digest_member WHERE kind=$1 AND NOT in_prev_set
			 AND (last_pushed_at IS NULL OR last_pushed_at < $2)`, kind, cutoff); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
