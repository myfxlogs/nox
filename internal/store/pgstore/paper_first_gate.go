// paper_first_gate.go —— D-593 补洞②：全模板纸面先行不变量闸。
// 真金执行路径统一闸点（ExecuteOpportunityInternal/提案 executed 写径）：
//
//	naked_short_vol → 结构性拒（负偏=权利金封顶下行不封顶，EA-05 判据）；
//	paper_exempt_d 非空 → 显式 D# 豁免过（D-286 承接位类）；
//	其余 → 须纸面证据行（paper_spot_carry/position_settlements 沙箱簿）
//	  在案才可真金——无证=「待纸面」拒。
//
// 判据落判据函数单源（PaperFirstGate）；模板列改表非代码 if。
package pgstore

import (
	"context"
	"fmt"
)

// PaperFirstGate 纸面先行判据（模板 id → 可真金/拒因）。
// evidenceProbe 注入：调用方供「本模板纸面证据在案」判据查询（不同模板证据
// 簿各异——carry=paper_spot_carry、dci=dci sim 订单面，pgstore 不臆造链）。
func (s *Store) PaperFirstGate(ctx context.Context, templateID string, hasPaperEvidence func() (bool, error)) (bool, string, error) {
	if templateID == "" {
		return false, "模板 id 缺——纸面先行闸无从判定", nil // fail-closed
	}
	var cls, exempt *string
	err := s.pool.QueryRow(ctx,
		`SELECT payoff_class, paper_exempt_d FROM fin_opp_template WHERE id=$1`, templateID).Scan(&cls, &exempt)
	if err != nil {
		return false, fmt.Sprintf("模板 %s 未注册——禁真金径", templateID), nil // 无行=未注册模板
	}
	if cls != nil && *cls == "naked_short_vol" {
		return false, fmt.Sprintf("模板 %s payoff_class=naked_short_vol——负偏结构禁真金径（EA-05）", templateID), nil
	}
	if exempt != nil && *exempt != "" {
		return true, "", nil // 显式 D# 豁免（列存非代码 if）
	}
	ok, err := hasPaperEvidence()
	if err != nil {
		return false, "", fmt.Errorf("paper evidence probe %s: %w", templateID, err)
	}
	if !ok {
		return false, fmt.Sprintf("模板 %s 无纸面证据行且无豁免——待纸面", templateID), nil
	}
	return true, "", nil
}

// HasPaperEvidence 模板级纸面证据判（D-593 复审⑧——证据域=paper_structures
// ∩ paper_spot_carry.structure；列 NULL/空=无映射结构零放行）。
func (s *Store) HasPaperEvidence(ctx context.Context, templateID string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM paper_spot_carry psc
		WHERE psc.structure = ANY(
			SELECT paper_structures FROM fin_opp_template WHERE id=$1)`, templateID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("paper evidence %s: %w", templateID, err)
	}
	return n > 0, nil
}
