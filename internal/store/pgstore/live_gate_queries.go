package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arb/internal/capital"

	"github.com/jackc/pgx/v5"
)

// GetLatestFactTimestamp D-195-C1 S3：返回最新 fact 时间戳（facts_fresh readiness 层用）。
// 无 fact → 返回 zero time（调用方按 NOT_READY 处理）。
func (s *Store) GetLatestFactTimestamp(ctx context.Context) (time.Time, error) {
	var ts time.Time
	err := s.pool.QueryRow(ctx, `SELECT MAX(ts) FROM facts`).Scan(&ts)
	if err != nil {
		return time.Time{}, fmt.Errorf("get latest fact ts: %w", err)
	}
	return ts, nil
}

// GetLatestCapitalSnapshotByAccountKey D-195-C1 S2 第 5 条：按 accountKey string 查最新 capital snapshot。
// 现有 CurrentCapitalSnapshot 按 accountID int64 查询；本方法按 scope_key（accountKey）查询。
// 不存在 → 返回 (nil, nil)。
func (s *Store) GetLatestCapitalSnapshotByAccountKey(ctx context.Context, accountKey string) (*capital.CapitalSnapshot, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT snapshot_id, scope_type, scope_key, account_id, source, source_event_id, source_ts, received_ts,
		       cash_usd, restricted_cash_usd, margin_used_usd, free_margin_usd, credit_usd,
		       liabilities_usd, accrued_fee_usd, unrealized_pnl_usd, liquidation_haircut_pct,
		       liquidatable_equity_usd, equity_usd, quality, raw_currency, raw_amount, fx_rate, fx_source, created_at
		FROM capital_snapshots
		WHERE scope_type = 'account' AND scope_key = $1
		ORDER BY source_ts DESC NULLS LAST, snapshot_id DESC LIMIT 1`, accountKey)
	var snap capital.CapitalSnapshot
	var sourceEventID *string
	var sourceTS *time.Time
	var receivedTS time.Time
	var createdAt time.Time
	err := row.Scan(&snap.SnapshotID, &snap.ScopeType, &snap.ScopeKey, &snap.AccountID,
		&snap.Source, &sourceEventID, &sourceTS, &receivedTS,
		&snap.CashUSD, &snap.RestrictedCashUSD, &snap.MarginUsedUSD, &snap.FreeMarginUSD, &snap.CreditUSD,
		&snap.LiabilitiesUSD, &snap.AccruedFeeUSD, &snap.UnrealizedPnLUSD, &snap.LiquidationHaircutPct,
		&snap.LiquidatableEquityUSD, &snap.EquityUSD, &snap.Quality, &snap.RawCurrency, &snap.RawAmount,
		&snap.FXRate, &snap.FXSource, &createdAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("latest capital snapshot by account_key=%s: %w", accountKey, err)
	}
	snap.SourceEventID = sourceEventID
	snap.SourceTS = sourceTS
	snap.ReceivedTS = receivedTS
	snap.CreatedAt = createdAt
	return &snap, nil
}

// GetAccountIsReal D-195-C1 S2 第 3 条：按 accountKey（venue）查 is_real 标记。
// 现有 ListAccountIsReal 返回全部 venue→*bool 映射；本方法按单个 venue 查询。
// venue 不存在或 is_real=NULL → 返回 (false, nil)（fail-closed：未知视为非实盘）。
// 调用方需区分"未知"与"demo"——返回 *bool 版本见 GetAccountIsRealPtr。
func (s *Store) GetAccountIsReal(ctx context.Context, accountKey string) (bool, error) {
	var isReal *bool
	err := s.pool.QueryRow(ctx, `SELECT is_real FROM broker_accounts WHERE venue = $1`, accountKey).Scan(&isReal)
	if err != nil {
		return false, fmt.Errorf("get account is_real venue=%s: %w", accountKey, err)
	}
	if isReal == nil {
		return false, nil // 未知 → false（fail-closed）
	}
	return *isReal, nil
}

// GetActiveHypothesis D-195-C1 S2 第 6 条：查 status='approved' 的最新 hypothesis。
// 现有 GetHypothesis 按 hypothesis_id 精确查询；本方法按 status 查最新。
// 不存在 → 返回 (nil, nil)。
func (s *Store) GetActiveHypothesis(ctx context.Context) (*capital.Hypothesis, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT hypothesis_id, hypothesis_version, status, net_return_hurdle, max_drawdown,
		       principal_rule, kill_rule, evidence_ref, approved_at
		FROM hypotheses WHERE status = 'approved'
		ORDER BY approved_at DESC NULLS LAST, updated_at DESC LIMIT 1`)
	var h capital.Hypothesis
	var evidenceRef *string
	var approvedAt *time.Time
	err := row.Scan(&h.HypothesisID, &h.HypothesisVersion, &h.Status, &h.NetReturnHurdle, &h.MaxDrawdown,
		&h.PrincipalRule, &h.KillRule, &evidenceRef, &approvedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get active hypothesis: %w", err)
	}
	h.EvidenceRef = evidenceRef
	h.ApprovedAt = approvedAt
	return &h, nil
}

// GetLatestOverallDrawdownPct D-195-C1 S2 第 7 条：按 scope_type/scope_key 查最新 overall 回撤百分比。
// 回撤从 capital_snapshots 表计算（capital_drawdowns 表不存在）。
// 现有 OverallDrawdown 按 accountIDs + time 计算；本方法按 scope 查最新 snapshot 计算。
// 无快照 → 返回 0（无回撤）。
func (s *Store) GetLatestOverallDrawdownPct(ctx context.Context, scopeType, scopeKey string) (float64, error) {
	// 读 overall scope 的 HWM + 当前 liquidatable_equity
	var hwm, current *float64
	err := s.pool.QueryRow(ctx, `
		SELECT MAX(liquidatable_equity_usd),
		       (SELECT liquidatable_equity_usd FROM capital_snapshots
		        WHERE scope_type = $1 AND scope_key = $2
		          AND quality IN ('actual','estimated')
		          AND liquidatable_equity_usd IS NOT NULL
		        ORDER BY source_ts DESC NULLS LAST, snapshot_id DESC LIMIT 1)
		FROM capital_snapshots
		WHERE scope_type = $1 AND scope_key = $2
		  AND quality IN ('actual','estimated')
		  AND liquidatable_equity_usd IS NOT NULL`,
		scopeType, scopeKey).Scan(&hwm, &current)
	if err != nil {
		return 0, fmt.Errorf("latest overall drawdown pct: %w", err)
	}
	if hwm == nil || current == nil || *hwm <= 0 {
		return 0, nil // 无数据或 HWM 非正 → 无回撤
	}
	if *current >= *hwm {
		return 0, nil // 当前 >= HWM → 无回撤
	}
	return (*hwm - *current) / *hwm * 100.0, nil
}
