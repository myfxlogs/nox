// broker_symbol_map 存储（docs/broker管理器实现方案.md §1）。
package pgstore

import (
	"context"
	"errors"
	"fmt"

	"arb/internal/notional"

	"github.com/jackc/pgx/v5"
)

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

// VenueCanonKey D-173：(venue, canonical) 复合键，product_group 查询用。
type VenueCanonKey struct {
	Venue     string
	Canonical string
}

// ProductGroupByVenueCanonical 返回 (venue, canonical) 的 product_group（众数聚合）。
// ok=false = 未映射或 product_group 为 NULL（未知，调用方放行）。
func (s *Store) ProductGroupByVenueCanonical(ctx context.Context, venue, canonical string) (string, bool, error) {
	var pg string
	err := s.pool.QueryRow(ctx, `
		SELECT product_group FROM broker_symbol_map
		WHERE venue = $1 AND canonical = $2 AND product_group IS NOT NULL
		GROUP BY product_group ORDER BY COUNT(*) DESC LIMIT 1
	`, venue, canonical).Scan(&pg)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return pg, true, nil
}

// ListProductGroups 批量返回所有 (venue, canonical) → product_group 映射（众数聚合）。
// discoverSwapCombos 用此批量预取。同 (venue, canonical) 多行取众数。
func (s *Store) ListProductGroups(ctx context.Context) (map[VenueCanonKey]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT venue, canonical, product_group FROM (
			SELECT venue, canonical, product_group,
			       ROW_NUMBER() OVER (PARTITION BY venue, canonical ORDER BY COUNT(*) DESC) AS rn
			FROM broker_symbol_map
			WHERE product_group IS NOT NULL
			GROUP BY venue, canonical, product_group
		) t WHERE rn = 1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[VenueCanonKey]string)
	for rows.Next() {
		var k VenueCanonKey
		var pg string
		if err := rows.Scan(&k.Venue, &k.Canonical, &pg); err != nil {
			return nil, err
		}
		out[k] = pg
	}
	return out, rows.Err()
}

// UpdateProductGroupByCanonical D-173：按 (venue, canonical) 批量更新 product_group。
// 一个 venue+canonical 可对应多行（多 broker_symbol），全部更新为同 product_group。
// productGroup 空字符串 → 不更新（跳过）。
func (s *Store) UpdateProductGroupByCanonical(ctx context.Context, venue, canonical, productGroup string) (int64, error) {
	if productGroup == "" {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE broker_symbol_map SET product_group = $3
		WHERE venue = $1 AND canonical = $2
	`, venue, canonical, productGroup)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
