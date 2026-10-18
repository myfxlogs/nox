package pgstore

import (
	"context"
	"fmt"
	"time"
)

// WatchRule watch_rule 行（业主视窗两件套 B：通用盯盘规则）。
type WatchRule struct {
	ID          int64
	Kind        string // 首期 'price'
	Subject     string // canonical symbol
	VenueKey    string // ''=全源
	Op          string // lt|le|gt|ge
	Threshold   float64
	FireMode    string // once|repeat
	CooldownSec int
	Note        string
	Level       string // info|warn|critical（空=warn——1049 加列）
	Message     string // 自定义文案（空=引擎默认格式串）
	Enabled     bool
	LastFiredAt *time.Time
}

// ListWatchRules 全规则读面（引擎+RPC 共用；created_at 序稳定）。
func (s *Store) ListWatchRules(ctx context.Context) ([]WatchRule, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, kind, subject, venue_key, op, threshold,
		fire_mode, cooldown_sec, note, level, message, enabled, last_fired_at
		FROM watch_rule ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list watch_rule: %w", err)
	}
	defer rows.Close()
	var out []WatchRule
	for rows.Next() {
		var r WatchRule
		if err := rows.Scan(&r.ID, &r.Kind, &r.Subject, &r.VenueKey, &r.Op, &r.Threshold,
			&r.FireMode, &r.CooldownSec, &r.Note, &r.Level, &r.Message, &r.Enabled, &r.LastFiredAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertWatchRule 规则增改：id=0 插入（RETURNING id）；id>0 按 id 更新全字段
// （id 不存在=错——前端零信任但后端仍 fail-closed 不静默插）。
func (s *Store) UpsertWatchRule(ctx context.Context, r WatchRule) (int64, error) {
	if r.Level == "" {
		r.Level = "warn" // 空串撞 CHECK——与引擎兜底同语义归一
	}
	if r.ID == 0 {
		var id int64
		err := s.pool.QueryRow(ctx, `INSERT INTO watch_rule
			(kind, subject, venue_key, op, threshold, fire_mode, cooldown_sec, note, level, message, enabled)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
			r.Kind, r.Subject, r.VenueKey, r.Op, r.Threshold, r.FireMode, r.CooldownSec, r.Note, r.Level, r.Message, r.Enabled).Scan(&id)
		return id, err
	}
	tag, err := s.pool.Exec(ctx, `UPDATE watch_rule SET
		kind=$2, subject=$3, venue_key=$4, op=$5, threshold=$6, fire_mode=$7,
		cooldown_sec=$8, note=$9, level=$10, message=$11, enabled=$12, updated_at=now() WHERE id=$1`,
		r.ID, r.Kind, r.Subject, r.VenueKey, r.Op, r.Threshold, r.FireMode, r.CooldownSec, r.Note, r.Level, r.Message, r.Enabled)
	if err != nil {
		return 0, fmt.Errorf("update watch_rule %d: %w", r.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return 0, fmt.Errorf("update watch_rule %d: 行不存在", r.ID)
	}
	return r.ID, nil
}

// DeleteWatchRule 删规则（不存在=错，同 Upsert fail-closed）。
func (s *Store) DeleteWatchRule(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM watch_rule WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete watch_rule %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("delete watch_rule %d: 行不存在", id)
	}
	return nil
}

// MarkWatchFired 触发落账：last_fired_at=now；once 规自翻 disabled（触发即退役留痕）。
func (s *Store) MarkWatchFired(ctx context.Context, id int64, disable bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE watch_rule SET last_fired_at=now(),
		enabled = CASE WHEN $2 THEN false ELSE enabled END, updated_at=now() WHERE id=$1`,
		id, disable)
	return err
}
