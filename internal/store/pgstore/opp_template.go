// opp_template.go —— D-185：机会模板册读取面（fin_opp_template+cond 子表；
// 纯规则库零执行写径——模板变更走迁移/D# 留痕，运行侧只读）。
package pgstore

import (
	"context"
	"fmt"
)

// OppTemplate 机会模板行（执行轨枚举 auto/semi/proposal 由 CHECK 约束）。
type OppTemplate struct {
	ID        string
	Mechanism string
	NetAPR    string
	ExecTrack string
	RiskLevel string
	Enabled   bool
	Conds     []OppTemplateCond
}

// OppTemplateCond 模板条件清单行（kind∈credential/capital/permission/constraint/holding，
// subject=venue|ccy|endpoint|field|asset，qualifier=cap/限值语义，min_val=数值界）。
type OppTemplateCond struct {
	Kind      string
	Subject   string
	Qualifier string
	MinVal    *float64
}

// ListOppTemplates 全模板+条件清单（enabled 排序稳定：id 升序）。
func (s *Store) ListOppTemplates(ctx context.Context) ([]OppTemplate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, mechanism, net_apr, exec_track, risk_level, enabled
		FROM fin_opp_template ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list opp templates: %w", err)
	}
	defer rows.Close()
	var out []OppTemplate
	idx := map[string]int{}
	for rows.Next() {
		var t OppTemplate
		if err := rows.Scan(&t.ID, &t.Mechanism, &t.NetAPR, &t.ExecTrack, &t.RiskLevel, &t.Enabled); err != nil {
			return nil, err
		}
		out = append(out, t)
		idx[t.ID] = len(out) - 1
	}
	crows, err := s.pool.Query(ctx, `
		SELECT template_id, kind, subject, qualifier, min_val
		FROM fin_opp_template_cond ORDER BY template_id, seq`)
	if err != nil {
		return nil, fmt.Errorf("list opp template conds: %w", err)
	}
	defer crows.Close()
	for crows.Next() {
		var tid string
		var c OppTemplateCond
		if err := crows.Scan(&tid, &c.Kind, &c.Subject, &c.Qualifier, &c.MinVal); err != nil {
			return nil, err
		}
		if i, ok := idx[tid]; ok {
			out[i].Conds = append(out[i].Conds, c)
		}
	}
	return out, rows.Err()
}
