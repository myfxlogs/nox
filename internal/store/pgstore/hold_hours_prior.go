// hold_hours_prior.go —— funding_carry hh 输入源修（D-642 发现一证实病灶：
// perp_perp paper exited 中位持有 3.0h=探针退出纪律非策略持有语义）。
// 持有窗判据落表：fin_opp_template.hold_hours_prior（1057 迁移）；NULL=无先验。
package pgstore

import (
	"context"
	"fmt"
)

// HoldHoursPrior 模板持有窗先验（小时）。模板行缺/列 NULL → ok=false 不编数。
func (s *Store) HoldHoursPrior(ctx context.Context, templateID string) (float64, bool, error) {
	var v *float64
	err := s.pool.QueryRow(ctx,
		`SELECT hold_hours_prior FROM fin_opp_template WHERE id=$1`, templateID).Scan(&v)
	if err != nil {
		return 0, false, fmt.Errorf("hold hours prior: %w", err)
	}
	if v == nil || *v <= 0 {
		return 0, false, nil
	}
	return *v, true, nil
}
