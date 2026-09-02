package pgstore

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ExecutionControl D-195 §3.1.1：执行控制当前状态。
type ExecutionControl struct {
	ControlID    int64
	ScopeType    string // global / account
	ScopeKey     string // global='' / account=account_key
	Mode         string // READ_ONLY / PAPER / TEST
	Enabled      bool
	ApprovedBy   string
	ConfirmedAt  *time.Time
	ExpiresAt    *time.Time
	RevokedAt    *time.Time
	Reason       string
	ConfigDigest string
	UpdatedAt    time.Time
}

// ExecutionControlEvent D-195 §3.1.1：执行控制 append-only 事件。
type ExecutionControlEvent struct {
	ID          int64
	ScopeType   string
	ScopeKey    string
	FromMode    string
	ToMode      string
	Actor       string
	Reason      string
	EvidenceRef string
	OccurredAt  time.Time
	EventKey    string
}

// GetExecutionControl 读取控制状态（global 或 account）。
// 不存在 → 返回 (nil, nil)（调用方按 READ_ONLY fail-closed 处理）。
func (s *Store) GetExecutionControl(ctx context.Context, scopeType, scopeKey string) (*ExecutionControl, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT control_id, scope_type, scope_key, mode, enabled, approved_by,
		       confirmed_at, expires_at, revoked_at, reason, config_digest, updated_at
		FROM execution_controls
		WHERE scope_type = $1 AND scope_key = $2`, scopeType, scopeKey)
	var c ExecutionControl
	err := row.Scan(&c.ControlID, &c.ScopeType, &c.ScopeKey, &c.Mode, &c.Enabled, &c.ApprovedBy,
		&c.ConfirmedAt, &c.ExpiresAt, &c.RevokedAt, &c.Reason, &c.ConfigDigest, &c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// UpsertExecutionControl 写入/更新控制状态 + 追加事件（单事务）。
// event 可为 nil（仅 upsert 不追加事件）。
func (s *Store) UpsertExecutionControl(ctx context.Context, c *ExecutionControl, event *ExecutionControlEvent) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 读取当前 mode（用于事件 from_mode）
	var fromMode string
	_ = tx.QueryRow(ctx, `SELECT mode FROM execution_controls WHERE scope_type=$1 AND scope_key=$2`, c.ScopeType, c.ScopeKey).Scan(&fromMode)

	_, err = tx.Exec(ctx, `
		INSERT INTO execution_controls (scope_type, scope_key, mode, enabled, approved_by, confirmed_at, expires_at, revoked_at, reason, config_digest, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (scope_type, scope_key) DO UPDATE SET
			mode = EXCLUDED.mode,
			enabled = EXCLUDED.enabled,
			approved_by = EXCLUDED.approved_by,
			confirmed_at = EXCLUDED.confirmed_at,
			expires_at = EXCLUDED.expires_at,
			revoked_at = EXCLUDED.revoked_at,
			reason = EXCLUDED.reason,
			config_digest = EXCLUDED.config_digest,
			updated_at = now()`,
		c.ScopeType, c.ScopeKey, c.Mode, c.Enabled, c.ApprovedBy, c.ConfirmedAt, c.ExpiresAt, c.RevokedAt, c.Reason, c.ConfigDigest)
	if err != nil {
		return err
	}

	if event != nil {
		if event.FromMode == "" {
			event.FromMode = fromMode
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO execution_control_events (scope_type, scope_key, from_mode, to_mode, actor, reason, evidence_ref, occurred_at, event_key)
			VALUES ($1, $2, $3, $4, $5, $6, $7, now(), $8)
			ON CONFLICT (event_key) DO NOTHING`,
			event.ScopeType, event.ScopeKey, event.FromMode, event.ToMode, event.Actor, event.Reason, event.EvidenceRef, event.EventKey)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// ListExecutionControls 列出所有控制状态。
func (s *Store) ListExecutionControls(ctx context.Context) ([]ExecutionControl, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT control_id, scope_type, scope_key, mode, enabled, approved_by,
		       confirmed_at, expires_at, revoked_at, reason, config_digest, updated_at
		FROM execution_controls ORDER BY scope_type, scope_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExecutionControl
	for rows.Next() {
		var c ExecutionControl
		if err := rows.Scan(&c.ControlID, &c.ScopeType, &c.ScopeKey, &c.Mode, &c.Enabled, &c.ApprovedBy,
			&c.ConfirmedAt, &c.ExpiresAt, &c.RevokedAt, &c.Reason, &c.ConfigDigest, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AppendExecutionControlEvent 追加事件（幂等：event_key 冲突 DO NOTHING）。
func (s *Store) AppendExecutionControlEvent(ctx context.Context, e *ExecutionControlEvent) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO execution_control_events (scope_type, scope_key, from_mode, to_mode, actor, reason, evidence_ref, occurred_at, event_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now(), $8)
		ON CONFLICT (event_key) DO NOTHING`,
		e.ScopeType, e.ScopeKey, e.FromMode, e.ToMode, e.Actor, e.Reason, e.EvidenceRef, e.EventKey)
	return err
}

// ListExecutionControlEvents 列出事件历史。
func (s *Store) ListExecutionControlEvents(ctx context.Context, scopeType, scopeKey string) ([]ExecutionControlEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, scope_type, scope_key, from_mode, to_mode, actor, reason, evidence_ref, occurred_at, event_key
		FROM execution_control_events
		WHERE scope_type = $1 AND scope_key = $2
		ORDER BY occurred_at DESC`, scopeType, scopeKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExecutionControlEvent
	for rows.Next() {
		var e ExecutionControlEvent
		if err := rows.Scan(&e.ID, &e.ScopeType, &e.ScopeKey, &e.FromMode, &e.ToMode, &e.Actor, &e.Reason, &e.EvidenceRef, &e.OccurredAt, &e.EventKey); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
