// broker_accounts / broker_symbol_map 存储（docs/broker管理器实现方案.md §1）。
package pgstore

import (
	"context"
	"time"
)

// BrokerAccount broker_accounts 行。
// Venue 为登录账号号（D-033 短化，原 {server}-{user} 复合格式已废弃）。
type BrokerAccount struct {
	Venue     string
	Platform  int // 0=MT4, 1=MT5
	Host      string
	Port      int
	User      int64
	Password  string
	Server    string
	Alias     string
	Status    string // connected / failed / stopped
	CreatedAt time.Time
}

// SymbolMapping broker_symbol_map 行。
type SymbolMapping struct {
	Venue        string
	BrokerSymbol string
	Canonical    string
}

// InsertBrokerAccount 插入账号；venue 冲突返回错误（UNIQUE 约束）。
func (s *Store) InsertBrokerAccount(ctx context.Context, a *BrokerAccount) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO broker_accounts (venue, platform, host, port, user_id, password, server, alias, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, '', $8)
	`, a.Venue, a.Platform, a.Host, a.Port, a.User, a.Password, a.Server, a.Status)
	return err
}

// ListBrokerAccounts 全量账号列表。
func (s *Store) ListBrokerAccounts(ctx context.Context) ([]BrokerAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT venue, platform, host, port, user_id, password, server, alias, status, created_at
		FROM broker_accounts ORDER BY created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BrokerAccount
	for rows.Next() {
		var a BrokerAccount
		if err := rows.Scan(&a.Venue, &a.Platform, &a.Host, &a.Port, &a.User, &a.Password,
			&a.Server, &a.Alias, &a.Status, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetBrokerAccount 按 venue 取单条账号。
func (s *Store) GetBrokerAccount(ctx context.Context, venue string) (*BrokerAccount, error) {
	var a BrokerAccount
	err := s.pool.QueryRow(ctx, `
		SELECT venue, platform, host, port, user_id, password, server, alias, status, created_at
		FROM broker_accounts WHERE venue = $1
	`, venue).Scan(&a.Venue, &a.Platform, &a.Host, &a.Port, &a.User, &a.Password,
		&a.Server, &a.Alias, &a.Status, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// DeleteBrokerAccount 按 venue 删除账号。
func (s *Store) DeleteBrokerAccount(ctx context.Context, venue string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM broker_accounts WHERE venue = $1`, venue)
	return err
}

// SetBrokerAccountAlias 设置别名；空串 = 清除。
func (s *Store) SetBrokerAccountAlias(ctx context.Context, venue, alias string) error {
	_, err := s.pool.Exec(ctx, `UPDATE broker_accounts SET alias = $1 WHERE venue = $2`, alias, venue)
	return err
}

// SetBrokerAccountStatus 更新连接状态。
func (s *Store) SetBrokerAccountStatus(ctx context.Context, venue, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE broker_accounts SET status = $1 WHERE venue = $2`, status, venue)
	return err
}

// UpsertSymbolMapping 写入映射（venue+broker_symbol 冲突时更新 canonical）。
func (s *Store) UpsertSymbolMapping(ctx context.Context, m *SymbolMapping) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO broker_symbol_map (venue, broker_symbol, canonical)
		VALUES ($1, $2, $3)
		ON CONFLICT (venue, broker_symbol) DO UPDATE SET canonical = EXCLUDED.canonical
	`, m.Venue, m.BrokerSymbol, m.Canonical)
	return err
}

// DeleteSymbolMapping 删除单条映射。
func (s *Store) DeleteSymbolMapping(ctx context.Context, venue, brokerSymbol string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM broker_symbol_map WHERE venue = $1 AND broker_symbol = $2`, venue, brokerSymbol)
	return err
}

// DeleteVenueSymbolMappings 删除 venue 全部映射（RemoveBroker 时清理）。
func (s *Store) DeleteVenueSymbolMappings(ctx context.Context, venue string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM broker_symbol_map WHERE venue = $1`, venue)
	return err
}

// ListSymbolMappings 映射列表；venue 空 = 全部。
func (s *Store) ListSymbolMappings(ctx context.Context, venue string) ([]SymbolMapping, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT venue, broker_symbol, canonical FROM broker_symbol_map
		WHERE ($1 = '' OR venue = $1)
		ORDER BY venue, broker_symbol
	`, venue)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SymbolMapping
	for rows.Next() {
		var m SymbolMapping
		if err := rows.Scan(&m.Venue, &m.BrokerSymbol, &m.Canonical); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
