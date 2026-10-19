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

// TrialPromotable 单 combo 晋级评估：
//
//	fills    —— positions.note 携带 `combo#<id>` 标的真仓 fill 数 ≥阈
//	           （fill→归因写径=续件，未落前恒 0 → 如实不晋级）；
//	cost_dev —— fin_cost_model 该 combo 双腿 venue 行 Σ|drift_bps| ≤阈，
//	           零行=实测成本数据缺 → 不晋级（fail-closed，禁虚报）；
//	critical —— 提及本 combo 的 critical 告警零残留（正则词界防撞 combo#200）。
//
// 返回值=可晋级/未达因（机读可查——晋升裁定消费面）。
func (s *Store) TrialPromotable(ctx context.Context, comboID int64) (bool, string, error) {
	minFills, ok, err := s.AppConfigFloat(ctx, "trial_min_fills")
	if err != nil || !ok {
		return false, "trial_min_fills 键缺", fmt.Errorf("trial_min_fills: %w ok=%v", err, ok)
	}
	costMax, ok, err := s.AppConfigFloat(ctx, "trial_cost_dev_max_bps")
	if err != nil || !ok {
		return false, "trial_cost_dev_max_bps 键缺", fmt.Errorf("trial_cost_dev_max_bps: %w ok=%v", err, ok)
	}

	var fills int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM positions WHERE note ~ ('combo#'||$1::text||'([^0-9]|$)')`, comboID).Scan(&fills); err != nil {
		return false, "", fmt.Errorf("trial fills: %w", err)
	}
	if float64(fills) < minFills {
		return false, fmt.Sprintf("fills %d<%.0f", fills, minFills), nil
	}

	var costRows int
	var costDev float64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*), COALESCE(sum(abs(drift_bps)),0) FROM fin_cost_model
		 WHERE venue IN (SELECT venue_buy FROM xasset_allow_combos WHERE id=$1
		                 UNION SELECT venue_sell FROM xasset_allow_combos WHERE id=$1)`, comboID,
	).Scan(&costRows, &costDev); err != nil {
		return false, "", fmt.Errorf("trial cost: %w", err)
	}
	if costRows == 0 {
		return false, "cost_dev 数据缺（fin_cost_model 零行——实收落账写径=续件）", nil
	}
	if costDev > costMax {
		return false, fmt.Sprintf("cost_dev %.1fbps>%.0fbps", costDev, costMax), nil
	}

	var critN int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='critical'
		 AND message ~ ('combo#'||$1::text||'([^0-9]|$)')`, comboID).Scan(&critN); err != nil {
		return false, "", fmt.Errorf("trial crit: %w", err)
	}
	if critN > 0 {
		return false, fmt.Sprintf("残差 critical=%d", critN), nil
	}
	return true, "fills/cost/crit 三判据实测达", nil
}

// PromoteComboTier trial→armed 迁线动作（判据达=TrialPromotable 真；原子单写
// +audit 告警同事务外放——tier 翻 armed 后下拍按 armed 风控硬线全量评）。
func (s *Store) PromoteComboTier(ctx context.Context, comboID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE xasset_allow_combos SET tier='armed', updated_at=now() WHERE id=$1 AND tier='trial'`, comboID)
	if err != nil {
		return false, fmt.Errorf("promote combo tier: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// TrialComboID trial 组合最小读面（晋级扫件取键）。
type TrialComboID struct {
	ID  int64
	Key string // canonical_buy→canonical_sell|venue_buy×venue_sell（告警键）
}

// ListTrialComboIDs enabled trial 组合 ID+键（晋级判据扫件数据源）。
func (s *Store) ListTrialComboIDs(ctx context.Context) ([]TrialComboID, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, canonical_buy||'→'||canonical_sell||'|'||venue_key_buy||'×'||venue_key_sell
		 FROM xasset_allow_combos WHERE enabled AND tier='trial'`)
	if err != nil {
		return nil, fmt.Errorf("list trial combos: %w", err)
	}
	defer rows.Close()
	var out []TrialComboID
	for rows.Next() {
		var r TrialComboID
		if err := rows.Scan(&r.ID, &r.Key); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
