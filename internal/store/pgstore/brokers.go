// broker_accounts / broker_symbol_map 存储（docs/broker管理器实现方案.md §1）。
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arb/internal/notional"

	"github.com/jackc/pgx/v5"
)

// BrokerAccount broker_accounts 行。
// Venue 为登录账号号（D-033 短化，原 {server}-{user} 复合格式已废弃）。
// CommissionBps 为账号类型真实佣金（bps/腿，D-036）；nil = 未配置回退 config 默认。
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
	CreatedAt     time.Time
}

// SymbolMapping broker_symbol_map 行。
// ContractSize 该 broker 符号真实合约规格（MT5 SymbolInfo.ContractSize，D-042）；
// nil = 未知（MT4 / 历史映射），校验跳过。
// Source 映射来源（D-140 S1）：manual=人工录入 / rule=规则库自动补录 / meta=MT5 元数据采集。
type SymbolMapping struct {
	Venue        string
	BrokerSymbol string
	Canonical    string
	ContractSize *float64
	Source       string // D-140 S1：manual/rule/meta，空=manual（DEFAULT 兜底）
}

// InsertBrokerAccount 插入账号；venue 冲突返回错误（UNIQUE 约束）。
func (s *Store) InsertBrokerAccount(ctx context.Context, a *BrokerAccount) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO broker_accounts (venue, platform, host, port, user_id, password, server, alias, status, commission_bps)
		VALUES ($1, $2, $3, $4, $5, $6, $7, '', $8, $9)
	`, a.Venue, a.Platform, a.Host, a.Port, a.User, a.Password, a.Server, a.Status, a.CommissionBps)
	return err
}

// ListBrokerAccounts 全量账号列表。
func (s *Store) ListBrokerAccounts(ctx context.Context) ([]BrokerAccount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT venue, platform, host, port, user_id, password, server, alias, status, commission_bps, created_at
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
			&a.Server, &a.Alias, &a.Status, &a.CommissionBps, &a.CreatedAt); err != nil {
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
		SELECT venue, platform, host, port, user_id, password, server, alias, status, commission_bps, created_at
		FROM broker_accounts WHERE venue = $1
	`, venue).Scan(&a.Venue, &a.Platform, &a.Host, &a.Port, &a.User, &a.Password,
		&a.Server, &a.Alias, &a.Status, &a.CommissionBps, &a.CreatedAt)
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

// UpsertSymbolMapping 写入映射（venue+broker_symbol 冲突时更新 canonical + contract_size + source）。
// ContractSize nil 写 NULL（未知）。Source 空=manual（DB DEFAULT 兜底）。
func (s *Store) UpsertSymbolMapping(ctx context.Context, m *SymbolMapping) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, contract_size, source)
		VALUES ($1, $2, $3, $4, COALESCE($5, 'manual'))
		ON CONFLICT (venue, broker_symbol) DO UPDATE SET canonical = EXCLUDED.canonical, contract_size = EXCLUDED.contract_size, source = EXCLUDED.source
	`, m.Venue, m.BrokerSymbol, m.Canonical, m.ContractSize, m.Source)
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
		SELECT venue, broker_symbol, canonical, contract_size, source FROM broker_symbol_map
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
		if err := rows.Scan(&m.Venue, &m.BrokerSymbol, &m.Canonical, &m.ContractSize, &m.Source); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CanonicalContractProfile 返回 canonical 的期望合约规格（跨全部 venue 该 canonical
// 已映射符号合约的众数；D-042）。ok=false = 无已知合约（首次映射/全 NULL），调用方跳过校验。
// 校验强制同标的跨 broker 合约一致，故正常数据众数唯一；多值异常取频次最高者防阻塞。
func (s *Store) CanonicalContractProfile(ctx context.Context, canonical string) (float64, bool, error) {
	var (
		val float64
		n   int
	)
	err := s.pool.QueryRow(ctx, `
		SELECT contract_size, COUNT(*) FROM broker_symbol_map
		WHERE canonical = $1 AND contract_size IS NOT NULL
		GROUP BY contract_size ORDER BY COUNT(*) DESC, contract_size LIMIT 1
	`, canonical).Scan(&val, &n)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return val, true, nil
}

// ContractSizeByCanonical 返回 venue 侧该 canonical 映射的合约规格（D-062 显示层 USD 派生）。
// 取该 venue+canonical 行的 contract_size（D-061 对称桶保证同 canonical 跨 venue CS 一致，
// 故 venue_buy 侧即代表）。ok=false = 未映射/CS 未知（NULL），调用方回退 0。
// D-087 §3.1：入口处 notional.VenueAccount 归一 venue 键（"277842155:mt5"→"277842155"），
// 全部消费方（verification/显示层/pipeline 装配）一次修好。
func (s *Store) ContractSizeByCanonical(ctx context.Context, venue, canonical string) (float64, bool, error) {
	venue = notional.VenueAccount(venue)
	var val float64
	err := s.pool.QueryRow(ctx, `
		SELECT contract_size FROM broker_symbol_map
		WHERE venue = $1 AND canonical = $2 AND contract_size IS NOT NULL
		LIMIT 1
	`, venue, canonical).Scan(&val)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return val, true, nil
}

// ContractSizeByBrokerSymbol 按 (venueKey, broker_symbol) 查 contract_size（D-126 P2）。
// review.go notionalFromLegs 拿到的是 PositionLeg.Venue（venueKey）和 PositionLeg.Symbol（broker_symbol），
// 非 canonical——需用 broker_symbol 查。venueKey 经 notional.VenueAccount 归一。
func (s *Store) ContractSizeByBrokerSymbol(ctx context.Context, venueKey, brokerSymbol string) (float64, bool, error) {
	venue := notional.VenueAccount(venueKey)
	var val float64
	err := s.pool.QueryRow(ctx, `
		SELECT contract_size FROM broker_symbol_map
		WHERE venue = $1 AND broker_symbol = $2 AND contract_size IS NOT NULL
		LIMIT 1
	`, venue, brokerSymbol).Scan(&val)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return val, true, nil
}

// BackfillSymbolContracts 为 venue 下 contract_size 为 NULL 的既有映射补写权威合约
// （broker 自取，D-042 后启动时回填；历史映射先于合约采集功能存在，NULL 使规格画像为空 →
// 校验形同虚设）。未命中符号保持 NULL（合约未知，跳过校验，合法）。
func (s *Store) BackfillSymbolContracts(ctx context.Context, venue string, contracts map[string]float64) error {
	for sym, cs := range contracts {
		if _, err := s.pool.Exec(ctx, `
			UPDATE broker_symbol_map SET contract_size = $1
			WHERE venue = $2 AND broker_symbol = $3 AND contract_size IS NULL
		`, cs, venue, sym); err != nil {
			return fmt.Errorf("backfill %s.%s: %w", venue, sym, err)
		}
	}
	return nil
}

// USDPer 返回计价币→USD 的汇率（D-087 §3.1 FXRateProvider，返修包②七直盘全覆盖）。
//   - "USD" → (1.0, true) 基准
//   - USD-base 直盘（JPY/CHF/CAD）→ 1 / USD{ccy} bid（USDJPY/USDCHF/USDCAD）
//   - USD-quote 直盘（EUR/GBP/AUD/NZD）→ {ccy}USD bid（EURUSD/GBPUSD/AUDUSD/NZDUSD）
//   - ""/其他 → (0, false) 诚实缺省（未知=换算不可得，调用方记 skipped 不猜）
//
// 查询窗口：ticks 表 symbol=对应直盘、ts ≤ at 最近一条（24h 窗口）；miss → false 诚实跳过。
// 依据：七直盘全部在采（ticks 实证 EURUSD/GBPUSD/AUDUSD/NZDUSD/USDJPY/USDCHF/USDCAD 各 6.3k~12.7k 行）。
func (s *Store) USDPer(ctx context.Context, quoteCcy string, at time.Time) (float64, bool) {
	switch quoteCcy {
	case "USD":
		return 1.0, true
	case "JPY", "CHF", "CAD":
		// USD-base 直盘：USD per ccy = USD{ccy} bid（如 USDJPY=150 → 1 JPY = 1/150 USD）
		var rate float64
		err := s.pool.QueryRow(ctx, `
			SELECT bid FROM ticks
			WHERE symbol = $1 AND ts <= $2 AND ts > $2 - interval '24 hours'
			ORDER BY ts DESC LIMIT 1
		`, "USD"+quoteCcy, at).Scan(&rate)
		if err != nil || rate <= 0 {
			return 0, false
		}
		return 1.0 / rate, true
	case "EUR", "GBP", "AUD", "NZD":
		// USD-quote 直盘：USD per ccy = {ccy}USD bid（如 EURUSD=1.08 → 1 EUR = 1.08 USD）
		var rate float64
		err := s.pool.QueryRow(ctx, `
			SELECT bid FROM ticks
			WHERE symbol = $1 AND ts <= $2 AND ts > $2 - interval '24 hours'
			ORDER BY ts DESC LIMIT 1
		`, quoteCcy+"USD", at).Scan(&rate)
		if err != nil || rate <= 0 {
			return 0, false
		}
		return rate, true
	default:
		return 0, false
	}
}
