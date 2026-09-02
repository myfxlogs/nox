package pgstore

import (
	"context"
	"fmt"
	"time"

	"arb/internal/capital"
)

// GetDecisionChain D-193 审计 P1-13：反查单条决策的完整链（decision → position → legs → cashflows → settlement）。
func (s *Store) GetDecisionChain(ctx context.Context, decisionID int64) (*DecisionChain, error) {
	// 读决策
	var d capital.CapitalDecision
	var oppID *string
	err := s.pool.QueryRow(ctx,
		`SELECT decision_id, account_id, capital_snapshot_id, opportunity_id, hypothesis_id, hypothesis_version,
		        decision, from_state, to_state, trigger_fact_id, visible_event_watermark,
		        parent_decision_id, reversible, capital_before_usd, capital_change_usd, capital_after_usd,
		        expected_net_usd, expected_net_annual_pct, annualization_basis, risk_budget_usd,
		        reason, evidence_ref, config_digest, occurred_at, event_key
		 FROM capital_decisions WHERE decision_id = $1`, decisionID).Scan(
		&d.DecisionID, &d.AccountID, &d.CapitalSnapshotID, &oppID, &d.HypothesisID, &d.HypothesisVersion,
		&d.Decision, &d.FromState, &d.ToState, &d.TriggerFactID, &d.VisibleEventWatermark,
		&d.ParentDecisionID, &d.Reversible, &d.CapitalBeforeUSD, &d.CapitalChangeUSD, &d.CapitalAfterUSD,
		&d.ExpectedNetUSD, &d.ExpectedNetAnnualPct, &d.AnnualizationBasis, &d.RiskBudgetUSD,
		&d.Reason, &d.EvidenceRef, &d.ConfigDigest, &d.OccurredAt, &d.EventKey)
	if err != nil {
		return nil, fmt.Errorf("get decision chain decision=%d: %w", decisionID, err)
	}
	chain := &DecisionChain{Decision: &d}
	if oppID == nil {
		return chain, nil // 决策未关联具体持仓
	}
	d.OpportunityID = oppID
	// 读 position（通过 opportunity_id，不新增 pgstore 方法，直接查）
	pos, err := s.getPositionByOppID(ctx, *oppID)
	if err != nil {
		return chain, nil // position 不存在 → 返回已有 decision
	}
	chain.Position = pos
	// 读 legs
	legs, err := s.ListPositionLegs(ctx, pos.ID)
	if err == nil {
		chain.Legs = legs
	}
	// 读 cashflows
	cfs, err := s.ListCashflows(ctx, pos.ID)
	if err == nil {
		chain.Cashflows = cfs
	}
	// 读 settlement
	settle, err := s.GetSettlementByPos(ctx, pos.ID)
	if err == nil {
		chain.Settlement = settle
	}
	return chain, nil
}

// getPositionByOppID 通过 opportunity_id 查 position（capital_reports 内部用）。
func (s *Store) getPositionByOppID(ctx context.Context, oppID string) (*Position, error) {
	var p Position
	var expectedEntrySpreadBps, expectedSwapBpsDaily, expectedAnnualPct, realizedPnl, notionalUSD *float64
	var openedAt, closedAt, closingStartedAt *time.Time
	var uncertaintyReason *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, opportunity_id, type, status, COALESCE(expected_net_bps, 0),
		        expected_entry_spread_bps, expected_swap_bps_daily, expected_annual_pct,
		        realized_pnl, opened_at, closed_at, note, notional_usd, closing_started_at, uncertainty_reason
		 FROM positions WHERE opportunity_id = $1 LIMIT 1`, oppID).Scan(
		&p.ID, &p.OpportunityID, &p.Type, &p.Status, &p.ExpectedNetBps,
		&expectedEntrySpreadBps, &expectedSwapBpsDaily, &expectedAnnualPct,
		&realizedPnl, &openedAt, &closedAt, &p.Note, &notionalUSD, &closingStartedAt, &uncertaintyReason)
	if err != nil {
		return nil, err
	}
	p.ExpectedEntrySpreadBps = expectedEntrySpreadBps
	p.ExpectedSwapBpsDaily = expectedSwapBpsDaily
	p.ExpectedAnnualPct = expectedAnnualPct
	p.RealizedPnl = realizedPnl
	p.OpenedAt = openedAt
	p.ClosedAt = closedAt
	p.NotionalUSD = notionalUSD
	p.ClosingStartedAt = closingStartedAt
	p.UncertaintyReason = uncertaintyReason
	return &p, nil
}
