// allow_rules.go D-443 S1：autotrade_allow_rules 加载器（venue/canonical 单表双 kind）。
// 加载失败 → 调用方 startup fatal（fail-closed 同 xasset 先例）；venue 空集=全拒（最保守），
// canonical 空集=全放行（D-122 语义不动）。simulated 闸对齐 environment（live 只读 false）。
package pgstore

import (
	"context"
	"fmt"
)

// LoadAutotradeAllowRules 按 kind 分流两 map（ venues/canonicals 均非 nil——空集合法态）。
func (s *Store) LoadAutotradeAllowRules(ctx context.Context, simulated bool) (venues, canonicals map[string]bool, err error) {
	rows, err := s.pool.Query(ctx, `
		SELECT kind, value FROM autotrade_allow_rules
		WHERE enabled AND simulated = $1`, simulated)
	if err != nil {
		return nil, nil, fmt.Errorf("load autotrade allow rules: %w", err)
	}
	defer rows.Close()
	venues, canonicals = map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			return nil, nil, err
		}
		switch kind {
		case "venue":
			venues[value] = true
		case "canonical":
			canonicals[value] = true
		}
	}
	return venues, canonicals, rows.Err()
}

// AutotradeAllowRule D-444 S2：autotrade_allow_rules 全列行（管理读面用）。
type AutotradeAllowRule struct {
	ID        int64
	Kind      string // venue / canonical
	Value     string
	Enabled   bool
	Simulated bool
	Note      string
}

// ListAutotradeAllowRulesAll 全量行（含 disabled+simulated；ORDER BY kind,value）。
func (s *Store) ListAutotradeAllowRulesAll(ctx context.Context) ([]AutotradeAllowRule, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, kind, value, enabled, simulated, note FROM autotrade_allow_rules ORDER BY kind, value`)
	if err != nil {
		return nil, fmt.Errorf("list autotrade allow rules: %w", err)
	}
	defer rows.Close()
	var out []AutotradeAllowRule
	for rows.Next() {
		var r AutotradeAllowRule
		if err := rows.Scan(&r.ID, &r.Kind, &r.Value, &r.Enabled, &r.Simulated, &r.Note); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetAutotradeAllowRuleEnabled 翻转 enabled（note 非空才覆写）；id 不存在→ErrNoRows 上抛。
func (s *Store) SetAutotradeAllowRuleEnabled(ctx context.Context, id int64, enabled bool, note string) (AutotradeAllowRule, error) {
	var r AutotradeAllowRule
	err := s.pool.QueryRow(ctx, `
		UPDATE autotrade_allow_rules
		SET enabled=$2, note=CASE WHEN $3 != '' THEN $3 ELSE note END
		WHERE id=$1
		RETURNING id, kind, value, enabled, simulated, note`,
		id, enabled, note).Scan(&r.ID, &r.Kind, &r.Value, &r.Enabled, &r.Simulated, &r.Note)
	if err != nil {
		return r, fmt.Errorf("set autotrade allow rule #%d: %w", id, err)
	}
	return r, nil
}
