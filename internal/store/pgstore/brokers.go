// broker_accounts 存储（docs/broker管理器实现方案.md §1）。
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrBrokerAccountNotFound venue 不存在于 broker_accounts（D-388 S0.2：UPDATE 0 行命中
// 不再静默返回 nil——静默无操作让前端停用点击以 2xx 假成功收尾）。errors.Is 判定。
var ErrBrokerAccountNotFound = errors.New("broker account venue not found")

// BrokerVenueDiscoverable D-388 S1：发现层配对 venue 过滤谓词（autotrade/scan 共用收口，
// 禁复制逻辑）——venue ∈ statuses 且 ≠"connected" → false（停用/失败 broker 立即退出配对，
// 不等 facts 自然过期）；venue ∉ statuses（CEX/adapter 自管 venue 不在 broker_accounts）→ true 放行。
func BrokerVenueDiscoverable(statuses map[string]string, venue string) bool {
	st, ok := statuses[venue]
	return !ok || st == "connected"
}

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
	IsReal        *bool  // D-144 ① 三态：nil=未知（不配对）
	SwapFree      bool   // D-014 S1：账号免 swap（true→carry 计算该 venue swap=0/account_flag，不回退 spec）
	Website       string // D-157 V3：broker 官网（DB 存储，业主自助管理；空 = 未设置）
	CreatedAt     time.Time
}

// InsertBrokerAccount 插入账号；venue 冲突返回错误（UNIQUE 约束）。
// IsReal *bool：nil=未知(NULL), &true=实盘, &false=demo（D-144 ① 三态）。
func (s *Store) InsertBrokerAccount(ctx context.Context, a *BrokerAccount) error {
	if a.Platform != 1 {
		return fmt.Errorf("platform=%d 非法——MT4 已退役（资产面零户），仅 mt5=1", a.Platform)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, alias, status, commission_bps, is_real, website)
		VALUES ($1, 'mt5', $2, $3, $4, $5, '', $6, $7, $8, $9)
	`, a.Venue, a.Host, a.Port, a.Password, a.Server, a.Status, a.CommissionBps, a.IsReal, a.Website)
	return err
}

// ListBrokerAccounts 全量账号列表。
func (s *Store) ListBrokerAccounts(ctx context.Context) ([]BrokerAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT venue, CASE venue_type WHEN 'mt4' THEN 0 ELSE 1 END AS platform, host, port,
		       venue::bigint AS user_id, password, server, alias, status, commission_bps, is_real, created_at, website,
		       COALESCE(swap_free, false)
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
			&a.Server, &a.Alias, &a.Status, &a.CommissionBps, &a.IsReal, &a.CreatedAt, &a.Website, &a.SwapFree); err != nil {
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
		SELECT venue, CASE venue_type WHEN 'mt4' THEN 0 ELSE 1 END AS platform, host, port,
		       venue::bigint AS user_id, password, server, alias, status, commission_bps, is_real, created_at, website,
		       COALESCE(swap_free, false)
		FROM broker_accounts WHERE venue = $1
	`, venue).Scan(&a.Venue, &a.Platform, &a.Host, &a.Port, &a.User, &a.Password,
		&a.Server, &a.Alias, &a.Status, &a.CommissionBps, &a.IsReal, &a.CreatedAt, &a.Website, &a.SwapFree)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// SetBrokerCommission 更新账号真实佣金（bps/腿，D-036）；nil 清除回退默认。
func (s *Store) SetBrokerCommission(ctx context.Context, venue string, bps *float64) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE broker_accounts SET commission_bps = $1 WHERE venue = $2`, bps, venue)
	if err == nil && tag.RowsAffected() == 0 {
		err = fmt.Errorf("%w: %s", ErrBrokerAccountNotFound, venue)
	}
	return err
}

// DeleteBrokerAccount 按 venue 删除账号。
func (s *Store) DeleteBrokerAccount(ctx context.Context, venue string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM broker_accounts WHERE venue = $1`, venue)
	return err
}

// SetBrokerAccountAlias 设置别名；空串 = 清除。
func (s *Store) SetBrokerAccountAlias(ctx context.Context, venue, alias string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE broker_accounts SET alias = $1 WHERE venue = $2`, alias, venue)
	if err == nil && tag.RowsAffected() == 0 {
		err = fmt.Errorf("%w: %s", ErrBrokerAccountNotFound, venue)
	}
	return err
}

// VenueSymbolMapCounts D-493 S5：venue→broker_symbol_map 映射条数（账户卡片删除知情确认透出用）。
func (s *Store) VenueSymbolMapCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT venue, COUNT(*) FROM broker_symbol_map GROUP BY venue`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]int)
	for rows.Next() {
		var venue string
		var n int
		if err := rows.Scan(&venue, &n); err != nil {
			return nil, err
		}
		m[venue] = n
	}
	return m, rows.Err()
}

// UpdateBrokerCredentials D-493 S4：凭据编辑——空串/0 = 不改动。password 只允许写、永不回读。
func (s *Store) UpdateBrokerCredentials(ctx context.Context, venue, host string, port int, server, password string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE broker_accounts
		SET host = COALESCE(NULLIF($2, ''), host),
		    port = COALESCE(NULLIF($3, 0), port),
		    server = COALESCE(NULLIF($4, ''), server),
		    password = COALESCE(NULLIF($5, ''), password)
		WHERE venue = $1`, venue, host, port, server, password)
	if err == nil && tag.RowsAffected() == 0 {
		err = fmt.Errorf("%w: %s", ErrBrokerAccountNotFound, venue)
	}
	return err
}

// SetBrokerWebsite 设置 broker 官网；空串 = 清除（D-157 V3.1）。
func (s *Store) SetBrokerWebsite(ctx context.Context, venue, website string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE broker_accounts SET website = $1 WHERE venue = $2`, website, venue)
	if err == nil && tag.RowsAffected() == 0 {
		err = fmt.Errorf("%w: %s", ErrBrokerAccountNotFound, venue)
	}
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
	tag, err := s.pool.Exec(ctx, `UPDATE broker_accounts SET status = $1 WHERE venue = $2`, status, venue)
	if err == nil && tag.RowsAffected() == 0 {
		err = fmt.Errorf("%w: %s", ErrBrokerAccountNotFound, venue)
	}
	return err
}

// ListBrokerAccountStatuses 返回 venue → status（mt-health D-216：窄查询，不取凭据列）。
func (s *Store) ListBrokerAccountStatuses(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT venue, status FROM broker_accounts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := make(map[string]string)
	for rows.Next() {
		var venue, status string
		if err := rows.Scan(&venue, &status); err != nil {
			return nil, err
		}
		m[venue] = status
	}
	return m, rows.Err()
}

// BrokerSwapFree D-014 S1：账号免 swap 旗（venue=bare 账号号；无行→false）。
// 查询失败 fail-closed 返回 error（调用方回退正常 swap 链=保守高估成本）。
func (s *Store) BrokerSwapFree(ctx context.Context, venue string) (bool, error) {
	var free bool
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(swap_free, false) FROM broker_accounts WHERE venue = $1`, venue).Scan(&free)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return free, err
}

// BrokerCommissionBps D-014 S2：账号真实佣金（bps/腿；econ 费率源=venue 事实）。
// 无行/NULL → ok=false fail-closed（禁回退 config 默认——缺费率即拒开）。
func (s *Store) BrokerCommissionBps(ctx context.Context, venue string) (float64, bool, error) {
	var v *float64
	err := s.pool.QueryRow(ctx,
		`SELECT commission_bps FROM broker_accounts WHERE venue = $1`, venue).Scan(&v)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if v == nil {
		return 0, false, nil
	}
	return *v, true, nil
}
