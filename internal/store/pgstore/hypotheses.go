package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arb/internal/capital"
	"github.com/jackc/pgx/v5"
)

// UpsertHypothesis 创建或更新假设。
func (s *Store) UpsertHypothesis(ctx context.Context, h *capital.Hypothesis) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO hypotheses
		   (hypothesis_id, hypothesis_version, status, net_return_hurdle, max_drawdown,
		    principal_rule, kill_rule, evidence_ref, approved_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
		 ON CONFLICT (hypothesis_id) DO UPDATE SET
		   hypothesis_version = EXCLUDED.hypothesis_version,
		   status = EXCLUDED.status,
		   net_return_hurdle = EXCLUDED.net_return_hurdle,
		   max_drawdown = EXCLUDED.max_drawdown,
		   principal_rule = EXCLUDED.principal_rule,
		   kill_rule = EXCLUDED.kill_rule,
		   evidence_ref = EXCLUDED.evidence_ref,
		   approved_at = EXCLUDED.approved_at,
		   updated_at = now()`,
		h.HypothesisID, h.HypothesisVersion, h.Status, h.NetReturnHurdle, h.MaxDrawdown,
		h.PrincipalRule, h.KillRule, h.EvidenceRef, h.ApprovedAt)
	if err != nil {
		return fmt.Errorf("upsert hypothesis %s: %w", h.HypothesisID, err)
	}
	return nil
}

// GetHypothesis 读假设。
func (s *Store) GetHypothesis(ctx context.Context, hypID string) (*capital.Hypothesis, error) {
	var h capital.Hypothesis
	var evidenceRef *string
	var approvedAt *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT hypothesis_id, hypothesis_version, status, net_return_hurdle, max_drawdown,
		        principal_rule, kill_rule, evidence_ref, approved_at
		 FROM hypotheses WHERE hypothesis_id = $1`, hypID).Scan(
		&h.HypothesisID, &h.HypothesisVersion, &h.Status, &h.NetReturnHurdle, &h.MaxDrawdown,
		&h.PrincipalRule, &h.KillRule, &evidenceRef, &approvedAt)
	if err != nil {
		return nil, fmt.Errorf("get hypothesis %s: %w", hypID, err)
	}
	h.EvidenceRef = evidenceRef
	h.ApprovedAt = approvedAt
	return &h, nil
}

// AppendCapitalDecision 追加资本决策（幂等：同 event_key 不重复）。
// 返回 won=true 表示新写入；won=false 表示已存在（幂等成功）。
func (s *Store) AppendCapitalDecision(ctx context.Context, d *capital.CapitalDecision) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO capital_decisions
		   (account_id, capital_snapshot_id, opportunity_id, hypothesis_id, hypothesis_version,
		    decision, from_state, to_state, trigger_fact_id, visible_event_watermark,
		    parent_decision_id, reversible, capital_before_usd, capital_change_usd, capital_after_usd,
		    expected_net_usd, expected_net_annual_pct, annualization_basis, risk_budget_usd,
		    reason, evidence_ref, config_digest, occurred_at, event_key)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)
		 ON CONFLICT (event_key) DO NOTHING`,
		d.AccountID, d.CapitalSnapshotID, d.OpportunityID, d.HypothesisID, d.HypothesisVersion,
		d.Decision, d.FromState, d.ToState, d.TriggerFactID, d.VisibleEventWatermark,
		d.ParentDecisionID, d.Reversible, d.CapitalBeforeUSD, d.CapitalChangeUSD, d.CapitalAfterUSD,
		d.ExpectedNetUSD, d.ExpectedNetAnnualPct, d.AnnualizationBasis, d.RiskBudgetUSD,
		d.Reason, d.EvidenceRef, d.ConfigDigest, d.OccurredAt, d.EventKey)
	if err != nil {
		return false, fmt.Errorf("append capital decision event_key=%s: %w", d.EventKey, err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListCapitalDecisions 按账户列决策（时间倒序）。
func (s *Store) ListCapitalDecisions(ctx context.Context, accountID int64, limit int) ([]capital.CapitalDecision, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT decision_id, account_id, capital_snapshot_id, opportunity_id, hypothesis_id, hypothesis_version,
		        decision, from_state, to_state, trigger_fact_id, visible_event_watermark,
		        parent_decision_id, reversible, capital_before_usd, capital_change_usd, capital_after_usd,
		        expected_net_usd, expected_net_annual_pct, annualization_basis, risk_budget_usd,
		        reason, evidence_ref, config_digest, occurred_at, event_key
		 FROM capital_decisions WHERE account_id = $1
		 ORDER BY occurred_at DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("list capital decisions account=%d: %w", accountID, err)
	}
	defer rows.Close()
	var out []capital.CapitalDecision
	for rows.Next() {
		var d capital.CapitalDecision
		if err := rows.Scan(&d.DecisionID, &d.AccountID, &d.CapitalSnapshotID, &d.OpportunityID,
			&d.HypothesisID, &d.HypothesisVersion, &d.Decision, &d.FromState, &d.ToState,
			&d.TriggerFactID, &d.VisibleEventWatermark, &d.ParentDecisionID, &d.Reversible,
			&d.CapitalBeforeUSD, &d.CapitalChangeUSD, &d.CapitalAfterUSD,
			&d.ExpectedNetUSD, &d.ExpectedNetAnnualPct, &d.AnnualizationBasis, &d.RiskBudgetUSD,
			&d.Reason, &d.EvidenceRef, &d.ConfigDigest, &d.OccurredAt, &d.EventKey); err != nil {
			return nil, fmt.Errorf("scan capital decision: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// AppendDecision 实现 capital.CapitalRepository.AppendDecision（包装 AppendCapitalDecision）。
func (s *Store) AppendDecision(ctx context.Context, decision capital.CapitalDecision) error {
	_, err := s.AppendCapitalDecision(ctx, &decision)
	return err
}

// CurrentState 实现 capital.CapitalRepository.CurrentState。
// 读 capital_decisions 最新 to_state。无决策 → 返回 ("", ErrUnknownFact)。
func (s *Store) CurrentState(ctx context.Context, accountID int64) (string, error) {
	var toState *string
	err := s.pool.QueryRow(ctx,
		`SELECT to_state FROM capital_decisions
		 WHERE account_id = $1 AND to_state IS NOT NULL
		 ORDER BY occurred_at DESC LIMIT 1`, accountID).Scan(&toState)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", capital.ErrUnknownFact
		}
		return "", fmt.Errorf("current state account=%d: %w", accountID, err)
	}
	if toState == nil {
		return "", capital.ErrUnknownFact
	}
	return *toState, nil
}

// CurrentOverallState 实现 capital.CapitalRepository.CurrentOverallState。
// 多账户归约，取最坏状态（按状态优先级：QUARANTINED > REDUCING > CASH > HARVESTING > DEPLOYED > READY > CORE）。
// 无任何账户有决策 → 返回 ("", ErrUnknownFact)。
func (s *Store) CurrentOverallState(ctx context.Context, accountIDs []int64) (string, error) {
	if len(accountIDs) == 0 {
		return "", capital.ErrUnknownFact
	}
	// 状态优先级映射（数字越大越"坏"）
	rank := map[string]int{
		capital.StateCore:        1,
		capital.StateReady:       2,
		capital.StateDeployed:    3,
		capital.StateHarvesting:  4,
		capital.StateCash:        5,
		capital.StateReducing:    6,
		capital.StateQuarantined: 7,
	}
	worst := ""
	worstRank := 0
	for _, aid := range accountIDs {
		state, err := s.CurrentState(ctx, aid)
		if err != nil {
			if errors.Is(err, capital.ErrUnknownFact) {
				continue // 该账户无决策，跳过
			}
			return "", err
		}
		if r, ok := rank[state]; ok && r > worstRank {
			worstRank = r
			worst = state
		}
	}
	if worst == "" {
		return "", capital.ErrUnknownFact
	}
	return worst, nil
}
