package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"arb/internal/capital"
	"github.com/jackc/pgx/v5"
)

// D-193-B：CapitalAccount/Snapshot/Movement 持久化。
// 实现 capital.CapitalRepository 接口（全部 9 方法，5 个 D-193-C 占位）。
// D-193 审计 P2-1：internal/capital 不 import internal/store/pgstore（无循环依赖）。

// CreateCapitalAccount 创建资本账户。principal_usd 由 DB CHECK 约束固定为 1000。
func (s *Store) CreateCapitalAccount(ctx context.Context, accountKey, currency, environment string) (*capital.CapitalAccount, error) {
	row := s.pool.QueryRow(ctx,
		`INSERT INTO capital_accounts (account_key, currency, environment)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (account_key) DO NOTHING
		 RETURNING account_id, account_key, currency, principal_usd, status, environment, opened_at, closed_at`,
		accountKey, currency, environment)
	var acc capital.CapitalAccount
	var closedAt *time.Time
	if err := row.Scan(&acc.AccountID, &acc.AccountKey, &acc.Currency, &acc.PrincipalUSD,
		&acc.Status, &acc.Environment, &acc.OpenedAt, &closedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// ON CONFLICT DO NOTHING → 已存在，读取现有
			return s.GetCapitalAccount(ctx, accountKey)
		}
		return nil, fmt.Errorf("create capital account %s: %w", accountKey, err)
	}
	acc.ClosedAt = closedAt
	return &acc, nil
}

// GetCapitalAccount 按 account_key 读取。
func (s *Store) GetCapitalAccount(ctx context.Context, accountKey string) (*capital.CapitalAccount, error) {
	var acc capital.CapitalAccount
	var closedAt *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT account_id, account_key, currency, principal_usd, status, environment, opened_at, closed_at
		 FROM capital_accounts WHERE account_key = $1`,
		accountKey).Scan(&acc.AccountID, &acc.AccountKey, &acc.Currency, &acc.PrincipalUSD,
		&acc.Status, &acc.Environment, &acc.OpenedAt, &closedAt)
	if err != nil {
		return nil, fmt.Errorf("get capital account %s: %w", accountKey, err)
	}
	acc.ClosedAt = closedAt
	return &acc, nil
}

// LinkVenueAccount 关联 venue 端点。
func (s *Store) LinkVenueAccount(ctx context.Context, accountID int64, venueAccountKey, environment string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO capital_account_venue_links (account_id, venue_account_key, environment)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (account_id, venue_account_key) DO NOTHING`,
		accountID, venueAccountKey, environment)
	if err != nil {
		return fmt.Errorf("link venue account %s to %d: %w", venueAccountKey, accountID, err)
	}
	return nil
}

// AppendCapitalSnapshot 追加快照（幂等：同 source_event_id 不重复）。
func (s *Store) AppendCapitalSnapshot(ctx context.Context, snap *capital.CapitalSnapshot) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO capital_snapshots
		   (scope_type, scope_key, account_id, source, source_event_id, source_ts, received_ts,
		    cash_usd, restricted_cash_usd, margin_used_usd, free_margin_usd, credit_usd,
		    liabilities_usd, accrued_fee_usd, unrealized_pnl_usd, liquidation_haircut_pct,
		    liquidatable_equity_usd, equity_usd, quality, raw_currency, raw_amount, fx_rate, fx_source)
		 VALUES ($1,$2,$3,$4,$5,$6,COALESCE($7,now()),
		         $8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
		 ON CONFLICT (scope_type, scope_key, source_event_id) WHERE source_event_id IS NOT NULL
		 DO NOTHING`,
		snap.ScopeType, snap.ScopeKey, snap.AccountID, snap.Source, snap.SourceEventID, snap.SourceTS, snap.ReceivedTS,
		snap.CashUSD, snap.RestrictedCashUSD, snap.MarginUsedUSD, snap.FreeMarginUSD, snap.CreditUSD,
		snap.LiabilitiesUSD, snap.AccruedFeeUSD, snap.UnrealizedPnLUSD, snap.LiquidationHaircutPct,
		snap.LiquidatableEquityUSD, snap.EquityUSD, snap.Quality, snap.RawCurrency, snap.RawAmount,
		snap.FXRate, snap.FXSource)
	if err != nil {
		return fmt.Errorf("append capital snapshot scope=%s key=%s: %w", snap.ScopeType, snap.ScopeKey, err)
	}
	return nil
}

// CurrentCapitalSnapshot 读最新快照。
func (s *Store) CurrentCapitalSnapshot(ctx context.Context, accountID int64) (*capital.CapitalSnapshot, error) {
	var snap capital.CapitalSnapshot
	var sourceEventID *string
	var sourceTS *time.Time
	var receivedTS time.Time
	var createdAt time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT snapshot_id, scope_type, scope_key, account_id, source, source_event_id, source_ts, received_ts,
		        cash_usd, restricted_cash_usd, margin_used_usd, free_margin_usd, credit_usd,
		        liabilities_usd, accrued_fee_usd, unrealized_pnl_usd, liquidation_haircut_pct,
		        liquidatable_equity_usd, equity_usd, quality, raw_currency, raw_amount, fx_rate, fx_source, created_at
		 FROM capital_snapshots
		 WHERE account_id = $1 AND scope_type = 'account'
		 ORDER BY source_ts DESC NULLS LAST, snapshot_id DESC
		 LIMIT 1`,
		accountID).Scan(&snap.SnapshotID, &snap.ScopeType, &snap.ScopeKey, &snap.AccountID,
		&snap.Source, &sourceEventID, &sourceTS, &receivedTS,
		&snap.CashUSD, &snap.RestrictedCashUSD, &snap.MarginUsedUSD, &snap.FreeMarginUSD, &snap.CreditUSD,
		&snap.LiabilitiesUSD, &snap.AccruedFeeUSD, &snap.UnrealizedPnLUSD, &snap.LiquidationHaircutPct,
		&snap.LiquidatableEquityUSD, &snap.EquityUSD, &snap.Quality, &snap.RawCurrency, &snap.RawAmount,
		&snap.FXRate, &snap.FXSource, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("current capital snapshot account=%d: %w", accountID, err)
	}
	snap.SourceEventID = sourceEventID
	snap.SourceTS = sourceTS
	snap.ReceivedTS = receivedTS
	snap.CreatedAt = createdAt
	return &snap, nil
}

// AppendCapitalMovement 追加资本流水（幂等：同 event_key 不重复）。
func (s *Store) AppendCapitalMovement(ctx context.Context, m *capital.CapitalMovement) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO ledger
		   (date, channel, currency, amount, fee_rate, tier, note,
		    account_id, kind, counterparty_account_id, raw_amount, raw_currency, amount_usd,
		    fx_rate, fx_source, external_event_id, event_key, quality, source, occurred_at, received_at)
		 VALUES (COALESCE($13, now()), COALESCE($12, ''), COALESCE($5, 'USD'), COALESCE($6, 0), 0, '', COALESCE($15, ''),
		         $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		 ON CONFLICT (event_key) WHERE event_key IS NOT NULL
		 DO NOTHING`,
		m.AccountID, m.Kind, m.CounterpartyAccountID, m.RawAmount, m.RawCurrency, m.AmountUSD,
		m.FXRate, m.FXSource, m.ExternalEventID, m.EventKey, m.Quality, m.Source,
		m.OccurredAt, m.ReceivedAt, m.Note)
	if err != nil {
		return fmt.Errorf("append capital movement: %w", err)
	}
	return nil
}

// D-193-C：以下 5 个方法已迁移到真实实现：
//   AppendDecision/CurrentState/CurrentOverallState → hypotheses.go
//   Drawdown/OverallDrawdown → capital_drawdown.go

// CurrentOverallSnapshot D-193 §5.1：读总体最新快照（scope_type=overall）。
// D-193-B 首版：按 scope_key 查最新 overall 快照。D-193-C 接入多账户归约。
func (s *Store) CurrentOverallSnapshot(ctx context.Context, accountIDs []int64) (capital.CapitalSnapshot, error) {
	var snap capital.CapitalSnapshot
	var sourceEventID *string
	var sourceTS *time.Time
	var receivedTS time.Time
	var createdAt time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT snapshot_id, scope_type, scope_key, account_id, source, source_event_id, source_ts, received_ts,
		        cash_usd, restricted_cash_usd, margin_used_usd, free_margin_usd, credit_usd,
		        liabilities_usd, accrued_fee_usd, unrealized_pnl_usd, liquidation_haircut_pct,
		        liquidatable_equity_usd, equity_usd, quality, raw_currency, raw_amount, fx_rate, fx_source, created_at
		 FROM capital_snapshots
		 WHERE scope_type = 'overall'
		 ORDER BY source_ts DESC NULLS LAST, snapshot_id DESC
		 LIMIT 1`).Scan(&snap.SnapshotID, &snap.ScopeType, &snap.ScopeKey, &snap.AccountID,
		&snap.Source, &sourceEventID, &sourceTS, &receivedTS,
		&snap.CashUSD, &snap.RestrictedCashUSD, &snap.MarginUsedUSD, &snap.FreeMarginUSD, &snap.CreditUSD,
		&snap.LiabilitiesUSD, &snap.AccruedFeeUSD, &snap.UnrealizedPnLUSD, &snap.LiquidationHaircutPct,
		&snap.LiquidatableEquityUSD, &snap.EquityUSD, &snap.Quality, &snap.RawCurrency, &snap.RawAmount,
		&snap.FXRate, &snap.FXSource, &createdAt)
	if err != nil {
		return capital.CapitalSnapshot{}, fmt.Errorf("current overall snapshot: %w", err)
	}
	snap.SourceEventID = sourceEventID
	snap.SourceTS = sourceTS
	snap.ReceivedTS = receivedTS
	snap.CreatedAt = createdAt
	return snap, nil
}

// AppendSnapshot 实现 capital.CapitalRepository.AppendSnapshot。
func (s *Store) AppendSnapshot(ctx context.Context, snapshot capital.CapitalSnapshot) error {
	return s.AppendCapitalSnapshot(ctx, &snapshot)
}

// AppendMovement 实现 capital.CapitalRepository.AppendMovement。
func (s *Store) AppendMovement(ctx context.Context, movement capital.CapitalMovement) error {
	return s.AppendCapitalMovement(ctx, &movement)
}

// CurrentSnapshot 实现 capital.CapitalRepository.CurrentSnapshot。
func (s *Store) CurrentSnapshot(ctx context.Context, accountID int64) (capital.CapitalSnapshot, error) {
	snap, err := s.CurrentCapitalSnapshot(ctx, accountID)
	if err != nil {
		return capital.CapitalSnapshot{}, err
	}
	return *snap, nil
}
