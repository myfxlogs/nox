package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// LiveGate D-195-C1 §3.2：LiveGate 持久记录（live_gates 表）。
type LiveGate struct {
	GateID        int64
	ScopeType     string // global/account
	ScopeKey      string
	AccountKey    string
	Strategy      string
	Operator      string
	ConfirmedAt   time.Time
	ExpiresAt     *time.Time
	RevokedAt     *time.Time
	RevokeReason  string
	ConfigDigest  string
	EvidenceRef   string
	CheckSnapshot []byte // JSONB 原始字节
	CreatedAt     time.Time
}

// GetActiveLiveGate D-195-C1 §3.2 第 1 条：读未撤销的 LiveGate 记录。
// 不存在 → 返回 (nil, nil)（调用方按"无 LiveGate"处理）。
func (s *Store) GetActiveLiveGate(ctx context.Context, scopeType, scopeKey string) (*LiveGate, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT gate_id, scope_type, scope_key, account_key, strategy, operator,
		       confirmed_at, expires_at, revoked_at, revoke_reason,
		       config_digest, evidence_ref, check_snapshot, created_at
		FROM live_gates
		WHERE scope_type = $1 AND scope_key = $2 AND revoked_at IS NULL
		ORDER BY created_at DESC LIMIT 1`, scopeType, scopeKey)
	var g LiveGate
	err := row.Scan(&g.GateID, &g.ScopeType, &g.ScopeKey, &g.AccountKey, &g.Strategy, &g.Operator,
		&g.ConfirmedAt, &g.ExpiresAt, &g.RevokedAt, &g.RevokeReason,
		&g.ConfigDigest, &g.EvidenceRef, &g.CheckSnapshot, &g.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get active live gate: %w", err)
	}
	return &g, nil
}

// InsertLiveGate D-195-C1：插入 LiveGate 记录（步骤 6 PilotApproval 调用，步骤 5 不调用）。
func (s *Store) InsertLiveGate(ctx context.Context, g *LiveGate) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO live_gates (scope_type, scope_key, account_key, strategy, operator,
			confirmed_at, expires_at, config_digest, evidence_ref, check_snapshot)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		g.ScopeType, g.ScopeKey, g.AccountKey, g.Strategy, g.Operator,
		g.ConfirmedAt, g.ExpiresAt, g.ConfigDigest, g.EvidenceRef, g.CheckSnapshot)
	if err != nil {
		return fmt.Errorf("insert live gate: %w", err)
	}
	return nil
}

// RevokeLiveGate D-195-C1 §3.2 第 8 条：撤销 LiveGate（标 revoked_at + reason）。
func (s *Store) RevokeLiveGate(ctx context.Context, scopeType, scopeKey, reason string) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE live_gates SET revoked_at = now(), revoke_reason = $3
		WHERE scope_type = $1 AND scope_key = $2 AND revoked_at IS NULL`,
		scopeType, scopeKey, reason)
	if err != nil {
		return fmt.Errorf("revoke live gate: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrTerminalState // 无可撤销的 active LiveGate
	}
	return nil
}
