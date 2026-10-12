// capability_constraint.go —— D-186②：fin_capability_constraint CRUD
// （托管份额/赎回 SLA/结算口径/风险禁限入库；scope 四枚举+nullable position_id）。
package pgstore

import (
	"context"
	"fmt"
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
