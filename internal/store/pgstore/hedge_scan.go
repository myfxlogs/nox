// hedge_scan.go —— 对冲腿可行面读面（oppmatch hedgeScan 用）：cfd_swap 假设列读、
// broker_symbol_map→asset_equiv 类桥、universe 对冲腿实读、无腿注记。
package pgstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SwapHypothesisRow cfd_swap 假设行（对冲扫描输入）。
type SwapHypothesisRow struct {
	ID          int64
	Venue       string
	CategoryKey string
}

// ListSwapHypotheses proposed 态 cfd_swap 假设集（rejected 弃件不重复扫描注记）。
func (s *Store) ListSwapHypotheses(ctx context.Context) ([]SwapHypothesisRow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, venue, category_key FROM fin_opp_hypothesis
		 WHERE category_key LIKE 'cfd_swap|%' AND status='proposed' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list swap hypotheses: %w", err)
	}
	defer rows.Close()
	var out []SwapHypothesisRow
	for rows.Next() {
		var r SwapHypothesisRow
		if err := rows.Scan(&r.ID, &r.Venue, &r.CategoryKey); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// BrokerSymbolCanonical MT 符号→canonical（broker_symbol_map 实读；'m' 后缀变体
// 一并查——facts 符号为裸名而映射表存 venue 原生名）。未命中→""零值。
func (s *Store) BrokerSymbolCanonical(ctx context.Context, venue, symbol string) (string, error) {
	var c string
	err := s.pool.QueryRow(ctx, `
		SELECT canonical FROM broker_symbol_map
		 WHERE venue=$1 AND (broker_symbol=$2 OR broker_symbol=$2||'m')
		 ORDER BY (broker_symbol=$2) DESC LIMIT 1`, venue, symbol).Scan(&c)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("broker canonical %s|%s: %w", venue, symbol, err)
	}
	return c, nil
}

// AssetClassOf canonical→asset_class（asset_equiv 实读；未入类→""零值）。
func (s *Store) AssetClassOf(ctx context.Context, canonical string) (string, error) {
	var c string
	err := s.pool.QueryRow(ctx,
		`SELECT asset_class FROM asset_equiv WHERE canonical=$1 AND enabled LIMIT 1`,
		canonical).Scan(&c)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("asset class %s: %w", canonical, err)
	}
	return c, nil
}

// HedgeLegRow CEX 对冲腿候选行（universe 实读）。
type HedgeLegRow struct {
	Venue, Symbol, ContractType string
	FundingAnn                  float64
	Spot                        bool
}

// HedgeLegsForClass asset_class→binance/okx/gate universe 同标的腿：
// asset_equiv.canonical 剥 quote 后缀=universe.base 对拍（XAUUSDT→XAU、
// PAXGUSDT→PAXG——venue 原生 base 实读不编映射）。contract_type=” 为现货腿。
func (s *Store) HedgeLegsForClass(ctx context.Context, class string) ([]HedgeLegRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.venue, u.symbol, COALESCE(u.contract_type,''),
		       COALESCE(u.last_funding_ann,0), (COALESCE(u.contract_type,'')='')
		FROM asset_equiv ae
		JOIN fin_cex_universe u
		  ON u.venue=ae.venue
		 AND u.base=regexp_replace(ae.canonical,'_?(USDT|USDC|USD_UM|USD)$','')
		WHERE ae.asset_class=$1 AND ae.enabled
		  AND ae.venue IN ('binance','okx','gate') AND u.status='TRADING'
		ORDER BY u.venue, u.symbol`, class)
	if err != nil {
		return nil, fmt.Errorf("hedge legs %s: %w", class, err)
	}
	defer rows.Close()
	var out []HedgeLegRow
	for rows.Next() {
		var r HedgeLegRow
		if err := rows.Scan(&r.Venue, &r.Symbol, &r.ContractType, &r.FundingAnn, &r.Spot); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkHypothesisNoHedge 无腿注记（detail 尾部幂等追加——「对冲腿：无」标记存库可稽）。
func (s *Store) MarkHypothesisNoHedge(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE fin_opp_hypothesis SET detail = detail || ' ｜对冲腿：无 CEX 同标的腿'
		 WHERE id=$1 AND detail NOT LIKE '%对冲腿：%'`, id)
	if err != nil {
		return fmt.Errorf("mark no-hedge %d: %w", id, err)
	}
	return nil
}

// LatestFactValue facts 单行最新值（kind+venue+symbol；缺行→err 由 caller 降级）。
func (s *Store) LatestFactValue(ctx context.Context, kind, venue, symbol string) (float64, error) {
	var v float64
	err := s.pool.QueryRow(ctx, `
		SELECT value FROM facts WHERE kind=$1 AND venue=$2 AND symbol=$3
		 ORDER BY ts DESC LIMIT 1`, kind, venue, symbol).Scan(&v)
	if err != nil {
		return 0, err
	}
	return v, nil
}
