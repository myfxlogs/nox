package pgstore

import (
	"context"
	"fmt"
	"time"
)

// AutotradeState 对应 autotrade_state 单行表（D-072 §3.2）。
type AutotradeState struct {
	State     string
	Reason    string
	HaltedOn  *time.Time
	UpdatedAt time.Time
}

// GetAutotradeState 读取状态机当前状态（单行表）。
func (s *Store) GetAutotradeState(ctx context.Context) (*AutotradeState, error) {
	var st AutotradeState
	err := s.pool.QueryRow(ctx, `
		SELECT state, reason, halted_on, updated_at FROM autotrade_state
		WHERE state = (SELECT state FROM autotrade_state LIMIT 1)`).Scan(
		&st.State, &st.Reason, &st.HaltedOn, &st.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get autotrade state: %w", err)
	}
	return &st, nil
}

// SetAutotradeState 更新状态机（单行 upsert）。
func (s *Store) SetAutotradeState(ctx context.Context, state, reason string, haltedOn *time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE autotrade_state SET state=$1, reason=$2, halted_on=$3, updated_at=now()`,
		state, reason, haltedOn)
	if err != nil {
		return fmt.Errorf("set autotrade state: %w", err)
	}
	return nil
}

// EnableStrategy 启用策略（disabled=false + reason 清空，D-072 §3.2 对称禁用/启用）。
func (s *Store) EnableStrategy(ctx context.Context, strategy string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO strategy_stats (strategy, disabled, disabled_reason, updated_at)
		VALUES ($1, false, '', now())
		ON CONFLICT (strategy) DO UPDATE SET disabled=false, disabled_reason='', updated_at=now()`,
		strategy)
	if err != nil {
		return fmt.Errorf("enable strategy: %w", err)
	}
	return nil
}
