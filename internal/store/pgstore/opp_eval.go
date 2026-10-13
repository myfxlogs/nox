// opp_eval.go —— D-258 M4.1：fin_opp_eval 评估卡读写面 + 证据查询。
// 一提案一卡（proposal_id PK upsert）；证据查询各源 venue/账本实表，
// 零行=NULL=缺证据（调用方判 unevaluable，不外推）。
package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// OppEval 评估卡行（五项拆解 NULL=缺证据项）。
type OppEval struct {
	ProposalID   int64
	TemplateID   string
	GrossAPR     *float64
	FeeAPR       *float64
	SlippageAPR  *float64
	FailureAPR   *float64
	WindowAPR    *float64
	NetAPR       *float64
	ActualAPR    *float64
	Verdict      string
	EvidenceRefs map[string]any
	ComputedAt   time.Time
}

// UpsertOppEval 一提案一卡幂等覆盖（重扫复算刷新；actual_apr 保已回写值）。
func (s *Store) UpsertOppEval(ctx context.Context, e *OppEval) error {
	ev, _ := json.Marshal(e.EvidenceRefs)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_opp_eval (proposal_id, template_id, gross_apr, fee_apr,
			slippage_apr, failure_apr, window_apr, net_apr, verdict, evidence_refs)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (proposal_id) DO UPDATE SET
			template_id=EXCLUDED.template_id, gross_apr=EXCLUDED.gross_apr,
			fee_apr=EXCLUDED.fee_apr, slippage_apr=EXCLUDED.slippage_apr,
			failure_apr=EXCLUDED.failure_apr, window_apr=EXCLUDED.window_apr,
			net_apr=EXCLUDED.net_apr, verdict=EXCLUDED.verdict,
			evidence_refs=EXCLUDED.evidence_refs, computed_at=now()`,
		e.ProposalID, e.TemplateID, e.GrossAPR, e.FeeAPR, e.SlippageAPR,
		e.FailureAPR, e.WindowAPR, e.NetAPR, e.Verdict, ev)
	return err
}

// GetOppEval 提案评估卡单查（无行=nil,nil——无卡）。
func (s *Store) GetOppEval(ctx context.Context, proposalID int64) (*OppEval, error) {
	var e OppEval
	err := s.pool.QueryRow(ctx, `
		SELECT proposal_id, template_id, gross_apr, fee_apr, slippage_apr,
		       failure_apr, window_apr, net_apr, actual_apr, verdict,
		       evidence_refs, computed_at
		FROM fin_opp_eval WHERE proposal_id=$1`, proposalID).Scan(
		&e.ProposalID, &e.TemplateID, &e.GrossAPR, &e.FeeAPR, &e.SlippageAPR,
		&e.FailureAPR, &e.WindowAPR, &e.NetAPR, &e.ActualAPR, &e.Verdict,
		&e.EvidenceRefs, &e.ComputedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// UpdateOppEvalActual 执行后回写实际 APR（§4.3 指标⑤预实偏差喂源）。
func (s *Store) UpdateOppEvalActual(ctx context.Context, proposalID int64, apr float64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE fin_opp_eval SET actual_apr=$2 WHERE proposal_id=$1`, proposalID, apr)
	return err
}

// —— 证据查询（各源实表；零行/NULL=false 缺证据）——

// LatestSpreadBps fin_cex_spread 最新行（6h 内）spread_bps——滑点证据。
func (s *Store) LatestSpreadBps(ctx context.Context, symbol string) (float64, bool, error) {
	var v float64
	err := s.pool.QueryRow(ctx, `
		SELECT spread_bps FROM fin_cex_spread
		WHERE symbol=$1 AND ts > now() - interval '6 hours'
		ORDER BY ts DESC LIMIT 1`, symbol).Scan(&v)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("latest spread bps: %w", err)
	}
	return v, true, nil
}

// MedianFactValue facts kind 值窗内中位数（同 MaxFactValue 的 value 口径）——市窗损耗证据。
func (s *Store) MedianFactValue(ctx context.Context, kind string, window time.Duration) (float64, bool, error) {
	var v *float64
	err := s.pool.QueryRow(ctx, `
		SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY value)
		FROM facts WHERE kind=$1 AND value IS NOT NULL
		  AND ts > now() - $2::interval`, kind, window).Scan(&v)
	if err != nil {
		return 0, false, fmt.Errorf("median fact %s: %w", kind, err)
	}
	if v == nil {
		return 0, false, nil
	}
	return *v, true, nil
}

// MedianEarnAPR fin_cex_earn_apr 窗内报价中位数——理财类市窗损耗证据。
func (s *Store) MedianEarnAPR(ctx context.Context, window time.Duration) (float64, bool, error) {
	var v *float64
	err := s.pool.QueryRow(ctx, `
		SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY apr)
		FROM fin_cex_earn_apr WHERE apr IS NOT NULL
		  AND ts > now() - $1::interval`, window).Scan(&v)
	if err != nil {
		return 0, false, fmt.Errorf("median earn apr: %w", err)
	}
	if v == nil {
		return 0, false, nil
	}
	return *v, true, nil
}

// DCIRejectCount fin_dci_order 败单数（failed/rejected，窗内）——失败成本证据。
func (s *Store) DCIRejectCount(ctx context.Context, templateID string, window time.Duration) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM fin_dci_order
		WHERE template_id=$1 AND status IN ('failed','rejected')
		  AND created_at > now() - $2::interval`, templateID, window).Scan(&n)
	return n, err
}
