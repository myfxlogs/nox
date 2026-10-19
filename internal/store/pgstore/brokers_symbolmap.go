// broker_symbol_map 存储（docs/broker管理器实现方案.md §1）。
package pgstore

import (
	"context"
	"errors"
	"fmt"

	"nox/internal/platform/notional"

	"github.com/jackc/pgx/v5"
)

// SymbolMapping broker_symbol_map 行。
// ContractSize 该 broker 符号真实合约规格（MT5 SymbolInfo.ContractSize，D-042）；
// nil = 未知（MT4 / 历史映射），校验跳过。
// Source 映射来源（D-140 S1）：manual=人工录入 / rule=规则库自动补录 / meta=MT5 元数据采集。
// IsPrimary D-383 A2.1：一 canonical 一主品种标记（false+组内恰一行=隐式 primary，语义同真）。
// PriceScale/QtyScale D-594 报价单位归一层：原生价×price_scale=canonical 价（USD/lb 口径），
// contract_size 原生单位×qty_scale=canonical 单位数/手。nil=未标定：单位敏感 canonical
// 未标定行抑制 canonical 发射 + 名义额 fail-closed；非敏感品种 nil 按 1.0 计。
type SymbolMapping struct {
	Venue        string
	BrokerSymbol string
	Canonical    string
	ContractSize *float64
	Source       string   // D-140 S1：manual/rule/meta，空=manual（DEFAULT 兜底）
	IsPrimary    bool     // D-383 A2.1
	PriceScale   *float64 // D-594：native price × scale = canonical price
	QtyScale     *float64 // D-594：contract_size × qty_scale = canonical units/lot
}

// UpsertSymbolMapping 写入映射（venue+broker_symbol 冲突时更新 canonical + contract_size + source）。
// ContractSize nil 写 NULL（未知）。Source 空=manual（DB DEFAULT 兜底）。
// is_primary 恒默认 false（D-383 A8 锚字面）：单行组靠隐式 primary 语义工作，
// 既有组的显式 primary 由 migration 0122 固化+种子表指定，多行组禁止默默猜。
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
		SELECT venue, broker_symbol, canonical, contract_size, source, is_primary, price_scale, qty_scale
		FROM broker_symbol_map
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
		if err := rows.Scan(&m.Venue, &m.BrokerSymbol, &m.Canonical, &m.ContractSize, &m.Source, &m.IsPrimary, &m.PriceScale, &m.QtyScale); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ResolvePrimarySymbol D-383 A2.4：执行路径 canonical → venue 原生符号解析（primary-only）。
// 规则=is_primary 或 (venue, canonical) 组内恰一行（隐式 primary）；多行组零 primary →
// 歧义态错误 `symbol resolution ambiguous (venue, canonical)`（fail-closed 宁拒单不撞车，
// 497 实证）；未映射/空 canonical → not mapped 错误。解析确定性：多合格行（唯一索引下不应
// 存在）按 broker_symbol 序取首。
func (s *Store) ResolvePrimarySymbol(ctx context.Context, venue, canonical string) (string, error) {
	if canonical == "" {
		return "", fmt.Errorf("symbol not mapped (%s, %s)", venue, canonical)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT broker_symbol, is_primary FROM broker_symbol_map
		WHERE venue = $1 AND canonical = $2
		ORDER BY broker_symbol
	`, venue, canonical)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type symRow struct {
		sym     string
		primary bool
	}
	var all []symRow
	for rows.Next() {
		var r symRow
		if err := rows.Scan(&r.sym, &r.primary); err != nil {
			return "", err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(all) == 0 {
		return "", fmt.Errorf("symbol not mapped (%s, %s)", venue, canonical)
	}
	for _, r := range all {
		if r.primary || len(all) == 1 {
			return r.sym, nil
		}
	}
	return "", fmt.Errorf("symbol resolution ambiguous (%s, %s)", venue, canonical)
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
// §3.1：入口处 notional.VenueAccount 归一 venue 键（"277842155:mt5"→"277842155"），
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

// CanonicalByBrokerSymbol 按 (venue, broker_symbol) 反查 canonical（D-430 margin-guard
// tick 键域：MT 腿 venue_symbol → ticks 存 canonical）。ok=false = 未映射（诚实 data_stale
// 由调用方落地）；venue 为裸账户号（venueKey 前缀），行唯一由 (venue,broker_symbol) 唯一索引保证。
func (s *Store) CanonicalByBrokerSymbol(ctx context.Context, venue, brokerSymbol string) (string, bool, error) {
	var c string
	err := s.pool.QueryRow(ctx, `
		SELECT canonical FROM broker_symbol_map
		WHERE venue = $1 AND broker_symbol = $2
	`, venue, brokerSymbol).Scan(&c)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return c, true, nil
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

// UpdateProductGroupByCanonical D-173：按 (venue, canonical) 批量更新 product_group。
// 一个 venue+canonical 可对应多行（多 broker_symbol），全部更新为同 product_group。
// productGroup 空字符串 → 不更新（跳过）。
// D-558：product_group 退役出配对判定（等价键改锚 fin_canonical_product_class），
// 本写径保留为 venue 原生分类存档（§5 腿级数据只认 venue 返回值，CalcMode 分类原样落库）。
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

// BrokerSymbolsByCanonical 反解：venue 内 canonical→原生 broker_symbol 集
// （facts 写侧键域）。canonical 比较走裸形（regexp 尾裁 USDT/USD——与 1055
// 组合行归一同口径：组合行裸形 TRX×map 全形 TRXUSDT 可互解）。真无映射=空集，
// 调用方保留 canonical 自查兜底（D-325 canonical 标签行语义）。
func (s *Store) BrokerSymbolsByCanonical(ctx context.Context, venue, bareCanonical string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT broker_symbol FROM broker_symbol_map
		WHERE venue = $1 AND regexp_replace(canonical, 'USDT$|USD$', '') = $2`,
		venue, bareCanonical)
	if err != nil {
		return nil, fmt.Errorf("broker symbols by canonical %s|%s: %w", venue, bareCanonical, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sym string
		if err := rows.Scan(&sym); err != nil {
			return nil, err
		}
		out = append(out, sym)
	}
	return out, rows.Err()
}
