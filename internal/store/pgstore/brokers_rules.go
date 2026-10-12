// broker_symbol_map 规则库自动补录（D-140 S1）+ 元数据反哺（D-140 S2）。
package pgstore

import (
	"context"
	"fmt"
	"log"
	"strings"

	"arb/internal/normalize"
)

// BackfillRuleMappings 规则库自动补录（D-140 S1）。
// 扫描 venue 下全部已知 broker_symbol，exact 未命中的跑规则库，
// 产出候选清单先落表 source='rule'（canonical 归一即生效，CS 留 NULL=CS 门继续拦）。
// 已有映射（含 source='rule'/'manual'/'meta'）跳过——幂等。
// 返回新增行数。
func (s *Store) BackfillRuleMappings(ctx context.Context, venue string, brokerSymbols []string, eng *normalize.RuleEngine) (int, error) {
	if eng == nil || len(brokerSymbols) == 0 {
		return 0, nil
	}
	existing, err := s.ListSymbolMappings(ctx, venue)
	if err != nil {
		return 0, fmt.Errorf("list existing mappings: %w", err)
	}
	mapped := make(map[string]bool, len(existing))
	for _, m := range existing {
		mapped[m.BrokerSymbol] = true
	}
	inserted := 0
	for _, sym := range brokerSymbols {
		if mapped[sym] {
			continue // 幂等：已有映射跳过
		}
		// D-176 反污染护栏：拒绝含 . 的 broker_symbol（股票代码格式，
		// normalize regex 误匹配的常见模式）。CFD 合法符号不含点号（EURUSD/XAGUSDm/US30_x10m 均无点号）；
		// 含点号的是股票/ETF（CLNE.NAS/GOLD.NYSE-24/HSIC.O）。
		if strings.Contains(sym, ".") {
			log.Printf("[normalize] backfill skip %s/%s: contains '.' (stock symbol format)", venue, sym)
			continue
		}
		canon, ok := eng.Match(sym)
		if !ok {
			continue // 规则未命中，留待人工
		}
		if err := s.UpsertSymbolMapping(ctx, &SymbolMapping{
			Venue:        venue,
			BrokerSymbol: sym,
			Canonical:    canon,
			ContractSize: nil, // CS 留 NULL=CS 门继续拦（红线：规则不产 CS）
			Source:       "rule",
		}); err != nil {
			log.Printf("[normalize] backfill %s/%s: %v", venue, sym, err)
			continue
		}
		inserted++
	}
	return inserted, nil
}

// UpsertSymbolMappingCSOnly D-140 S2：MT5 元数据采集反哺。
// 仅当 (venue, broker_symbol) 已存在且 contract_size IS NULL 时填 CS；
// 不存在行 → 插入 source='meta'（canonical 留空，待规则/人工补 canonical）；
// 已有非 NULL CS → 不覆盖（人工行优先，红线：meta 不覆盖人工）。
// 返回 (filled, inserted) = 填充已有行数 / 新插入行数。
func (s *Store) UpsertSymbolMappingCSOnly(ctx context.Context, venue, brokerSymbol string, cs float64) (filled, inserted int, err error) {
	if cs <= 0 {
		return 0, 0, nil // 无效 CS 跳过
	}
	// 先查现有行
	var existingCS *float64
	var hasRow bool
	err = s.pool.QueryRow(ctx, `
		SELECT contract_size FROM broker_symbol_map WHERE venue = $1 AND broker_symbol = $2
	`, venue, brokerSymbol).Scan(&existingCS)
	if err != nil {
		// 行不存在 → 插入 source='meta'（canonical 留空）
		_, err = s.pool.Exec(ctx, `
			INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, contract_size, source)
			VALUES ($1, $2, '', $3, 'meta')
			ON CONFLICT (venue, broker_symbol) DO NOTHING
		`, venue, brokerSymbol, cs)
		if err != nil {
			return 0, 0, fmt.Errorf("insert meta row: %w", err)
		}
		return 0, 1, nil
	}
	hasRow = true
	_ = hasRow
	// 行存在：仅 CS IS NULL 时填
	if existingCS == nil {
		_, err = s.pool.Exec(ctx, `
			UPDATE broker_symbol_map SET contract_size = $3
			WHERE venue = $1 AND broker_symbol = $2 AND contract_size IS NULL
		`, venue, brokerSymbol, cs)
		if err != nil {
			return 0, 0, fmt.Errorf("fill null CS: %w", err)
		}
		return 1, 0, nil
	}
	// 已有非 NULL CS → 不覆盖
	return 0, 0, nil
}

// SymbolCoverageRow 覆盖率视图单行（D-140 S2）：venue×canonical×状态。
type SymbolCoverageRow struct {
	Venue     string
	Canonical string
	Status    string // mapped+CS / mapped-no-CS / rule-only / unmapped
	Count     int
}

// SymbolCoverage 覆盖率视图（D-140 S2）：按 venue×canonical×状态聚合。
// 状态四态：
//   - mapped+CS    = 有映射且有 CS（可交易）
//   - mapped-no-CS = 有映射但 CS NULL（CS 门拦）
//   - rule-only    = source='rule'（规则补录，CS 必 NULL）
//   - unmapped     = MT5 服务器有符号但 broker_symbol_map 无映射
//
// unmapped 需调用方传入 broker 服务器符号表（DB 无此数据）。
func (s *Store) SymbolCoverage(ctx context.Context, venue string, serverSymbols []string) ([]SymbolCoverageRow, error) {
	// 已映射行按 canonical + 状态聚合
	rows, err := s.pool.Query(ctx, `
		SELECT venue, canonical,
		       CASE
		         WHEN contract_size IS NOT NULL AND contract_size > 0 THEN 'mapped+CS'
		         WHEN source = 'rule' THEN 'rule-only'
		         ELSE 'mapped-no-CS'
		       END,
		       COUNT(*)
		FROM broker_symbol_map
		WHERE ($1 = '' OR venue = $1)
		GROUP BY venue, canonical,
		         CASE
		         WHEN contract_size IS NOT NULL AND contract_size > 0 THEN 'mapped+CS'
		         WHEN source = 'rule' THEN 'rule-only'
		         ELSE 'mapped-no-CS'
		         END
		ORDER BY venue, canonical
	`, venue)
	if err != nil {
		return nil, fmt.Errorf("query coverage: %w", err)
	}
	defer rows.Close()
	var out []SymbolCoverageRow
	mappedSet := map[string]bool{} // venue+brokerSymbol → 已映射
	for rows.Next() {
		var r SymbolCoverageRow
		if err := rows.Scan(&r.Venue, &r.Canonical, &r.Status, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// unmapped：服务器有符号但 DB 无映射
	if len(serverSymbols) > 0 {
		existing, err := s.ListSymbolMappings(ctx, venue)
		if err != nil {
			return nil, fmt.Errorf("list existing for unmapped: %w", err)
		}
		for _, m := range existing {
			mappedSet[m.Venue+"|"+m.BrokerSymbol] = true
		}
		unmappedCount := 0
		for _, sym := range serverSymbols {
			if !mappedSet[venue+"|"+sym] {
				unmappedCount++
			}
		}
		if unmappedCount > 0 {
			out = append(out, SymbolCoverageRow{
				Venue:     venue,
				Canonical: "",
				Status:    "unmapped",
				Count:     unmappedCount,
			})
		}
	}
	return out, nil
}
