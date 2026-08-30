// broker_accounts 存储（docs/broker管理器实现方案.md §1）。
package pgstore

import (
	"context"
	"time"
)

// BrokerAccount broker_accounts 行。
// Venue 为登录账号号（D-033 短化，原 {server}-{user} 复合格式已废弃）。
// CommissionBps 为账号类型真实佣金（bps/腿，D-036）；nil = 未配置回退 config 默认。
// IsReal D-144 ① 三态：*bool（true=实盘, false=demo, nil=未知）。组合引擎同类型配对用。
type BrokerAccount struct {
	Venue         string
	Platform      int // 0=MT4, 1=MT5
	Host          string
	Port          int
	User          int64
	Password      string
	Server        string
	Alias         string
	Status        string // connected / failed / stopped
	CommissionBps *float64
	IsReal        *bool // D-144 ① 三态：nil=未知（不配对）
	Website       string // D-157 V3：broker 官网（DB 存储，业主自助管理；空 = 未设置）
	CreatedAt     time.Time
}

// InsertBrokerAccount 插入账号；venue 冲突返回错误（UNIQUE 约束）。
// IsReal *bool：nil=未知(NULL), &true=实盘, &false=demo（D-144 ① 三态）。
func (s *Store) InsertBrokerAccount(ctx context.Context, a *BrokerAccount) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO broker_accounts (venue, platform, host, port, user_id, password, server, alias, status, commission_bps, is_real, website)
		VALUES ($1, $2, $3, $4, $5, $6, $7, '', $8, $9, $10, $11)
	`, a.Venue, a.Platform, a.Host, a.Port, a.User, a.Password, a.Server, a.Status, a.CommissionBps, a.IsReal, a.Website)
	return err
}

// ListBrokerAccounts 全量账号列表。
func (s *Store) ListBrokerAccounts(ctx context.Context) ([]BrokerAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT venue, platform, host, port, user_id, password, server, alias, status, commission_bps, is_real, created_at, website
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
			&a.Server, &a.Alias, &a.Status, &a.CommissionBps, &a.IsReal, &a.CreatedAt, &a.Website); err != nil {
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
		SELECT venue, platform, host, port, user_id, password, server, alias, status, commission_bps, is_real, created_at, website
		FROM broker_accounts WHERE venue = $1
	`, venue).Scan(&a.Venue, &a.Platform, &a.Host, &a.Port, &a.User, &a.Password,
		&a.Server, &a.Alias, &a.Status, &a.CommissionBps, &a.IsReal, &a.CreatedAt, &a.Website)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// SetBrokerCommission 更新账号真实佣金（bps/腿，D-036）；nil 清除回退默认。
func (s *Store) SetBrokerCommission(ctx context.Context, venue string, bps *float64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE broker_accounts SET commission_bps = $1 WHERE venue = $2`, bps, venue)
	return err
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

// SetBrokerWebsite 设置 broker 官网；空串 = 清除（D-157 V3.1）。
func (s *Store) SetBrokerWebsite(ctx context.Context, venue, website string) error {
	_, err := s.pool.Exec(ctx, `UPDATE broker_accounts SET website = $1 WHERE venue = $2`, website, venue)
	return err
}

// ListAccountAliases 返回 venue→alias 映射（仅 alias 非空行；F3 Broker 实持仓账号列显示别名）。
func (s *Store) ListAccountAliases(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT venue, alias FROM broker_accounts WHERE alias <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var venue, alias string
		if err := rows.Scan(&venue, &alias); err != nil {
			return nil, err
		}
		m[venue] = alias
	}
	return m, rows.Err()
}

// SetBrokerAccountStatus 更新连接状态。
func (s *Store) SetBrokerAccountStatus(ctx context.Context, venue, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE broker_accounts SET status = $1 WHERE venue = $2`, status, venue)
	return err
}
