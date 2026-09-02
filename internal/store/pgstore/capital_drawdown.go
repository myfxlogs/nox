package pgstore

import (
	"context"
	"fmt"
	"time"

	"arb/internal/capital"
)

// Drawdown 实现 capital.CapitalRepository.Drawdown。
// 读 capital_snapshots 历史（scope_type='account'），找 HWM（max liquidatable_equity），调 capital.ComputeDrawdown。
// D-193 审计 P1-10：跳过 quality='unknown' 的快照——只用 quality='actual' 或 'estimated' 的快照计算 HWM 和当前净值。
// 全部快照 unknown（或无快照）→ 返回 ErrUnknownFact。
// estimated 快照计入计算但结果 WarningLevel 标注"含 estimated 数据"。
func (s *Store) Drawdown(ctx context.Context, accountID int64, at time.Time) (capital.Drawdown, error) {
	return s.drawdownByScope(ctx, "account", accountID, at)
}

// OverallDrawdown 实现 capital.CapitalRepository.OverallDrawdown。
// 读 overall scope 快照（scope_type='overall'），同 Drawdown 逻辑。
func (s *Store) OverallDrawdown(ctx context.Context, accountIDs []int64, at time.Time) (capital.Drawdown, error) {
	// overall 快照 scope_key 固定（D-193-B 实现），accountIDs 参数用于未来多账户归约。
	// D-193-C 首版：读 scope_type='overall' 的快照。
	return s.drawdownByScope(ctx, "overall", 0, at)
}

// drawdownByScope 按 scope_type 计算回撤。
// scopeType='account' 时 accountID 为账户 ID；scopeType='overall' 时 accountID 忽略。
func (s *Store) drawdownByScope(ctx context.Context, scopeType string, accountID int64, at time.Time) (capital.Drawdown, error) {
	// 查 quality='actual' 或 'estimated' 的快照，source_ts <= at
	// D-193 审计 P1-10：跳过 quality='unknown'
	var query string
	var args []interface{}
	if scopeType == "account" {
		query = `SELECT liquidatable_equity_usd, quality, source_ts
		         FROM capital_snapshots
		         WHERE scope_type = 'account' AND account_id = $1
		           AND quality IN ('actual','estimated')
		           AND liquidatable_equity_usd IS NOT NULL
		           AND (source_ts IS NULL OR source_ts <= $2)
		         ORDER BY source_ts DESC NULLS LAST, snapshot_id DESC`
		args = []interface{}{accountID, at}
	} else {
		query = `SELECT liquidatable_equity_usd, quality, source_ts
		         FROM capital_snapshots
		         WHERE scope_type = 'overall'
		           AND quality IN ('actual','estimated')
		           AND liquidatable_equity_usd IS NOT NULL
		           AND (source_ts IS NULL OR source_ts <= $1)
		         ORDER BY source_ts DESC NULLS LAST, snapshot_id DESC`
		args = []interface{}{at}
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return capital.Drawdown{}, fmt.Errorf("query drawdown scope=%s: %w", scopeType, err)
	}
	defer rows.Close()
	var hwm float64
	var currentEquity float64
	var hasEstimated bool
	var hasAny bool
	first := true
	for rows.Next() {
		var equity float64
		var quality string
		var sourceTS *time.Time
		if err := rows.Scan(&equity, &quality, &sourceTS); err != nil {
			return capital.Drawdown{}, fmt.Errorf("scan drawdown: %w", err)
		}
		if first {
			currentEquity = equity
			first = false
			hasAny = true
		}
		if equity > hwm {
			hwm = equity
		}
		if quality == "estimated" {
			hasEstimated = true
		}
	}
	if err := rows.Err(); err != nil {
		return capital.Drawdown{}, fmt.Errorf("rows iter drawdown: %w", err)
	}
	if !hasAny {
		return capital.Drawdown{}, capital.ErrUnknownFact
	}
	// net_external_capital 暂取 0（D-193-C 首版，外部资本归约待 D-196 Allocator 接入）
	dd := capital.ComputeDrawdown(hwm, currentEquity, 0)
	if hasEstimated {
		if dd.WarningLevel == "" {
			dd.WarningLevel = "含 estimated 数据"
		} else {
			dd.WarningLevel += "（含 estimated 数据）"
		}
	}
	return dd, nil
}

// ListDecisionsForAccount D-193 审计 P1-13：按账户列决策（时间倒序，带时间范围）。
func (s *Store) ListDecisionsForAccount(ctx context.Context, accountID int64, from, to time.Time, limit int) ([]capital.CapitalDecision, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT decision_id, account_id, capital_snapshot_id, opportunity_id, hypothesis_id, hypothesis_version,
		        decision, from_state, to_state, trigger_fact_id, visible_event_watermark,
		        parent_decision_id, reversible, capital_before_usd, capital_change_usd, capital_after_usd,
		        expected_net_usd, expected_net_annual_pct, annualization_basis, risk_budget_usd,
		        reason, evidence_ref, config_digest, occurred_at, event_key
		 FROM capital_decisions
		 WHERE account_id = $1 AND occurred_at BETWEEN $2 AND $3
		 ORDER BY occurred_at DESC LIMIT $4`, accountID, from, to, limit)
	if err != nil {
		return nil, fmt.Errorf("list decisions for account=%d: %w", accountID, err)
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
			return nil, fmt.Errorf("scan decision: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
