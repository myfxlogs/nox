// capability_constraint.go —— D-186②：fin_capability_constraint CRUD
// （托管份额/赎回 SLA/结算口径/风险禁限入库；scope 四枚举+nullable position_id）。
package pgstore

import (
	"context"
	"fmt"
	"strings"
)

// UpsertCapabilityConstraint 约束行增改（UNIQUE(scope,coalesce(position_id,0),subject) 幂等键）。
func (s *Store) UpsertCapabilityConstraint(ctx context.Context, c *CapabilityConstraint) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_capability_constraint
		  (scope, position_id, subject, qualifier, value_num, note, source)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (scope, (COALESCE(position_id, 0)), subject) DO UPDATE SET
		  qualifier=EXCLUDED.qualifier, value_num=EXCLUDED.value_num,
		  note=EXCLUDED.note, source=EXCLUDED.source, updated_at=now()`,
		c.Scope, c.PositionID, c.Subject, c.Qualifier, c.ValueNum, c.Note, c.Source)
	if err != nil {
		return fmt.Errorf("upsert capability constraint: %w", err)
	}
	return nil
}

// ListCapabilityConstraints 全约束行（scope+subject 稳定序）。
func (s *Store) ListCapabilityConstraints(ctx context.Context) ([]CapabilityConstraint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, scope, position_id, subject, qualifier, value_num, note, source
		FROM fin_capability_constraint ORDER BY scope, subject`)
	if err != nil {
		return nil, fmt.Errorf("list capability constraints: %w", err)
	}
	defer rows.Close()
	var out []CapabilityConstraint
	for rows.Next() {
		var c CapabilityConstraint
		if err := rows.Scan(&c.ID, &c.Scope, &c.PositionID, &c.Subject, &c.Qualifier,
			&c.ValueNum, &c.Note, &c.Source); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteCapabilityConstraint 删约束行（id 定向）。
func (s *Store) DeleteCapabilityConstraint(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM fin_capability_constraint WHERE id=$1`, id)
	return err
}

// FundingAbsentSubject funding_absent 能力账本键形（STATE 行50——venue 恒播
// fundingRate=0 的合约无 funding 面；scope='global' value_num=1 同 dormant 形）。
func FundingAbsentSubject(canonical, venue string) string {
	return "funding_absent:" + canonical + "@" + venue
}

// IsFundingAbsent 缺位标注查（在册=venue 无 funding 面——如实拒不撞 worst 闸）。
func (s *Store) IsFundingAbsent(ctx context.Context, canonical, venue string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM fin_capability_constraint
			WHERE scope='global' AND subject=$1 AND value_num=1)`,
		FundingAbsentSubject(canonical, venue)).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("is funding absent %s@%s: %w", canonical, venue, err)
	}
	return ok, nil
}

// ExecVenueSets venue 可执行面投影（D-651：exec_venue:<tmpl>@<venue> 行集——
// 账本有行域归 DB 驱动；全空=迁移未至回落 gate 静态集不臆断）。
func (s *Store) ExecVenueSets(ctx context.Context) (map[string]map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT subject FROM fin_capability_constraint
		WHERE scope='global' AND subject LIKE 'exec_venue:%' AND value_num=1`)
	if err != nil {
		return nil, fmt.Errorf("exec_venue 投影读败: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]bool{}
	for rows.Next() {
		var subj string
		if err := rows.Scan(&subj); err != nil {
			return nil, err
		}
		rest := strings.TrimPrefix(subj, "exec_venue:")
		tmpl, venue, ok2 := strings.Cut(rest, "@")
		if !ok2 || tmpl == "" || venue == "" {
			continue
		}
		if out[tmpl] == nil {
			out[tmpl] = map[string]bool{}
		}
		out[tmpl][venue] = true
	}
	return out, rows.Err()
}
