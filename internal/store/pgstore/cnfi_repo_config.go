package pgstore

// cnfi_repo_config.go D-270 R2：逆回购提醒配置 DB 化（单行表 id=1）。
// GetRepoConfig 读单行；SetRepoConfig UPDATE id=1。
// env 仍作 fallback（main.go 装配时若 DB 为默认值可用 env 覆盖）。

import (
	"context"
	"time"
)

// RepoConfig 对应 cnfi_repo_config 表（单行 id=1）。
type RepoConfig struct {
	IdleCny      float64
	MinReturnCny float64
	ReminderHour int
	Enabled      bool
	UpdatedAt    time.Time
}

// GetRepoConfig 读 cnfi_repo_config id=1。
func (s *Store) GetRepoConfig(ctx context.Context) (*RepoConfig, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT idle_cny, min_return_cny, reminder_hour, enabled, updated_at
		 FROM cnfi_repo_config WHERE id = 1`)
	var c RepoConfig
	if err := row.Scan(&c.IdleCny, &c.MinReturnCny, &c.ReminderHour, &c.Enabled, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// SetRepoConfig UPDATE id=1（单行表，无需 UPSERT，初始行已由 migration 插入）。
func (s *Store) SetRepoConfig(ctx context.Context, idleCny, minReturnCny float64, reminderHour int, enabled bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE cnfi_repo_config SET idle_cny = $1, min_return_cny = $2, reminder_hour = $3, enabled = $4, updated_at = now() WHERE id = 1`,
		idleCny, minReturnCny, reminderHour, enabled)
	return err
}
