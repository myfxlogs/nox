// exchange_configs 存储（docs/CEX交易所后台管理.md D-054）。
// 凭据明文存储（业主拍板，同 broker_accounts）；列表 RPC 只回掩码，secret 不回传（D-043）。
package pgstore

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// ExchangeConfig exchange_configs 行。
type ExchangeConfig struct {
	ID         int64
	Venue      string // binance / okx
	VenueType  string // spot
	Label      string
	APIKey     string
	APISecret  string
	Passphrase string
	BaseURL    string
	Simulated  bool   // 模拟盘标记位（D-516：demo 域收编后仅存数据语义——消费端 fail-closed 跳过）
	Website    string // D-157 V3：交易所官网（DB 存储；空 = 未设置）
	CreatedAt  time.Time
}

// InsertExchangeConfig 插入交易所配置并回填 id、created_at；venue+venue_type 冲突返回错误（UNIQUE 约束）。
func (s *Store) InsertExchangeConfig(ctx context.Context, c *ExchangeConfig) error {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO exchange_configs (venue, venue_type, label, api_key, api_secret, passphrase, base_url, simulated, website)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at
	`, c.Venue, c.VenueType, c.Label, c.APIKey, c.APISecret, c.Passphrase, c.BaseURL, c.Simulated, c.Website).Scan(&c.ID, &c.CreatedAt)
	return err
}

// ListExchangeConfigs 全量交易所配置（按创建时间序）。
func (s *Store) ListExchangeConfigs(ctx context.Context) ([]ExchangeConfig, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, venue, venue_type, label, api_key, api_secret, passphrase, base_url, simulated, created_at, website
		FROM exchange_configs ORDER BY created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExchangeConfig
	for rows.Next() {
		var c ExchangeConfig
		if err := rows.Scan(&c.ID, &c.Venue, &c.VenueType, &c.Label,
			&c.APIKey, &c.APISecret, &c.Passphrase, &c.BaseURL, &c.Simulated, &c.CreatedAt, &c.Website); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteExchangeConfig 按 id 删除。
func (s *Store) DeleteExchangeConfig(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM exchange_configs WHERE id = $1`, id)
	return err
}

// ErrExchangeConflict UNIQUE 冲突判定。
func ErrExchangeConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// SetExchangeLabel 设置交易所别名；空串 = 清除（D-157 V3）。
func (s *Store) SetExchangeLabel(ctx context.Context, id int64, label string) error {
	_, err := s.pool.Exec(ctx, `UPDATE exchange_configs SET label = $1 WHERE id = $2`, label, id)
	return err
}

// SetExchangeWebsite 设置交易所官网；空串 = 清除（D-157 V3.1）。
func (s *Store) SetExchangeWebsite(ctx context.Context, id int64, website string) error {
	_, err := s.pool.Exec(ctx, `UPDATE exchange_configs SET website = $1 WHERE id = $2`, website, id)
	return err
}
