package pgstore

// system_settings.go D-296：系统配置 DB 化（单行表 id=1，同 ai_settings 模式）。
// GetSystemSettings 读单行；SetSystemSettings UPSERT id=1（空 tronscan_base=不改动）。

import (
	"context"
	"time"
)

// SystemSettings 系统配置（单行表 id=1）。
type SystemSettings struct {
	TronscanBase string
	UpdatedAt    time.Time
}

// GetSystemSettings 读 system_settings id=1（表初始行 INSERT ON CONFLICT DO NOTHING 保证存在）。
func (s *Store) GetSystemSettings(ctx context.Context) (*SystemSettings, error) {
	row := s.pool.QueryRow(ctx, `SELECT tronscan_base, updated_at FROM system_settings WHERE id = 1`)
	var a SystemSettings
	var tronscanBase *string
	if err := row.Scan(&tronscanBase, &a.UpdatedAt); err != nil {
		return nil, err
	}
	if tronscanBase != nil {
		a.TronscanBase = *tronscanBase
	}
	return &a, nil
}

// SetSystemSettings UPSERT id=1。tronscanBase 空串=不改动（保留原值）。
func (s *Store) SetSystemSettings(ctx context.Context, tronscanBase string) error {
	if tronscanBase == "" {
		_, err := s.pool.Exec(ctx, `UPDATE system_settings SET updated_at = now() WHERE id = 1`)
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE system_settings SET tronscan_base = $1, updated_at = now() WHERE id = 1`, tronscanBase)
	return err
}
