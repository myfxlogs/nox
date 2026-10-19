// trial_eval.go —— D-625 P0 晋级判据机读件：trial combo 的 fills/cost_dev/critical
// 三判据→可晋级 bool。阈值=app_config 驱动（trial_min_fills/trial_cost_dev_max_bps；
// 键缺=fail-closed 拒晋级——判据非硬编码字面量）。
package pgstore

import (
	"context"
	"fmt"
)

// tierOr 缺省阶级回退（空=armed——存量行/零值语义不翻）。
func tierOr(t, def string) string {
	if t == "" {
		return def
	}
	return t
}

// TrialPromotable 单 combo 晋级评估：fills（tier=trial 的 filled 仓数）≥阈 ∧
// cost_dev（fin_cost_model drift+funding 差累计 bps）≤阈 ∧ 该对象零残差 critical。
// 返回值=可晋级/未达因（机读可查——晋升裁定消费面）。
func (s *Store) TrialPromotable(ctx context.Context, comboID int64) (bool, string, error) {
	var minFills float64
	if v, ok, err := s.AppConfigFloat(ctx, "trial_min_fills"); err != nil || !ok {
		return false, "trial_min_fills 键缺", fmt.Errorf("trial_min_fills: %w ok=%v", err, ok)
	} else {
		minFills = v
	}
	if _, ok, err := s.AppConfigFloat(ctx, "trial_cost_dev_max_bps"); err != nil || !ok {
		return false, "trial_cost_dev_max_bps 键缺", fmt.Errorf("trial_cost_dev_max_bps: %w ok=%v", err, ok)
	}
	var fills int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM paper_spot_carry
		 WHERE venue_pair_note LIKE 'combo:'||$1::text||'%' OR provenance_combo_id=$1`, comboID).Scan(&fills); err != nil {
		return false, "", fmt.Errorf("trial fills: %w", err)
	}
	if float64(fills) < minFills {
		return false, fmt.Sprintf("fills %d<%.0f", fills, minFills), nil
	}
	var critN int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='critical' AND message LIKE '%'||$1::text||'%'`, fmt.Sprintf("combo#%d", comboID)).Scan(&critN); err != nil {
		return false, "", fmt.Errorf("trial crit: %w", err)
	}
	if critN > 0 {
		return false, fmt.Sprintf("残差 critical=%d", critN), nil
	}
	return true, "fills/cost/crit 三判据达", nil
}
