// D-272 逆回购操作记录+策略验证闭环 Store 方法（S1）。
// cnfi_repo_trades 表管实际操作记录+理论对比+结算+偏差分析。
// 与 cnfi_repo_ops（D-270 提醒生命周期）分离。
package pgstore

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// RepoTradeRow 是 cnfi_repo_trades 表的一行（D-272）。
type RepoTradeRow struct {
	ID                      int64
	InstrumentCode          string
	TradeDate               time.Time
	MaturityDate            time.Time
	HoldingDays             int
	PrincipalCny            float64
	ActualRatePct           float64
	TheoreticalRatePct      *float64
	TheoreticalReturnByIdle *float64
	TheoreticalReturnCny    *float64
	IdleCnyAtRecord         *float64
	SignalSnapshot          *string
	ActualReturnCny         *float64
	SettledAt               *time.Time
	DeviationRateBps        *float64
	DeviationPrincipalCny   *float64
	DeviationReturnCny      *float64
	DeviationReturnPct      *float64
	Status                  string
	RepoOpID                *int64
	EntrySource             string
	LastMaturityRemindedAt  *time.Time
	Rationale               *string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// InsertRepoTrade 插入一条逆回购操作记录。
// 唯一约束冲突（trade_date + instrument_code 重复）→ 返回 pgx 错误，调用方判断。
func (s *Store) InsertRepoTrade(ctx context.Context, r *RepoTradeRow) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO cnfi_repo_trades (
			instrument_code, trade_date, holding_days, maturity_date,
			principal_cny, actual_rate_pct,
			theoretical_rate_pct, theoretical_return_by_idle, theoretical_return_cny,
			idle_cny_at_record, signal_snapshot,
			deviation_rate_bps, deviation_principal_cny,
			status, repo_op_id, entry_source, rationale
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 'open', $14, 'manual', $15)
		RETURNING id`,
		r.InstrumentCode, r.TradeDate.Format("2006-01-02"), r.HoldingDays, r.MaturityDate.Format("2006-01-02"),
		r.PrincipalCny, r.ActualRatePct,
		r.TheoreticalRatePct, r.TheoreticalReturnByIdle, r.TheoreticalReturnCny,
		r.IdleCnyAtRecord, r.SignalSnapshot,
		r.DeviationRateBps, r.DeviationPrincipalCny,
		r.RepoOpID, r.Rationale,
	).Scan(&id)
	return id, err
}

// ListRepoTrades 列出逆回购操作历史（按 trade_date DESC，前端列表）。
func (s *Store) ListRepoTrades(ctx context.Context, limit int, status string) ([]RepoTradeRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var query string
	var args []any
	if status != "" {
		query = `
			SELECT id, instrument_code, trade_date, maturity_date, holding_days,
			       principal_cny, actual_rate_pct,
			       theoretical_rate_pct, theoretical_return_by_idle, theoretical_return_cny,
			       idle_cny_at_record, signal_snapshot,
			       actual_return_cny, settled_at,
			       deviation_rate_bps, deviation_principal_cny,
			       deviation_return_cny, deviation_return_pct,
			       status, repo_op_id, entry_source, last_maturity_reminded_at,
			       rationale, created_at, updated_at
			FROM cnfi_repo_trades WHERE status = $1
			ORDER BY trade_date DESC, id DESC LIMIT $2`
		args = []any{status, limit}
	} else {
		query = `
			SELECT id, instrument_code, trade_date, maturity_date, holding_days,
			       principal_cny, actual_rate_pct,
			       theoretical_rate_pct, theoretical_return_by_idle, theoretical_return_cny,
			       idle_cny_at_record, signal_snapshot,
			       actual_return_cny, settled_at,
			       deviation_rate_bps, deviation_principal_cny,
			       deviation_return_cny, deviation_return_pct,
			       status, repo_op_id, entry_source, last_maturity_reminded_at,
			       rationale, created_at, updated_at
			FROM cnfi_repo_trades
			ORDER BY trade_date DESC, id DESC LIMIT $1`
		args = []any{limit}
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoTradeRow
	for rows.Next() {
		var r RepoTradeRow
		if err := scanRepoTradeRow(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRepoTrade 取单条记录（结算/删除前校验用）。
func (s *Store) GetRepoTrade(ctx context.Context, id int64) (*RepoTradeRow, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, instrument_code, trade_date, maturity_date, holding_days,
		       principal_cny, actual_rate_pct,
		       theoretical_rate_pct, theoretical_return_by_idle, theoretical_return_cny,
		       idle_cny_at_record, signal_snapshot,
		       actual_return_cny, settled_at,
		       deviation_rate_bps, deviation_principal_cny,
		       deviation_return_cny, deviation_return_pct,
		       status, repo_op_id, entry_source, last_maturity_reminded_at,
		       rationale, created_at, updated_at
		FROM cnfi_repo_trades WHERE id = $1`, id)
	var r RepoTradeRow
	if err := scanRepoTradeRowScan(row.Scan, &r); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// SettleRepoTrade 结算确认（更新 actual_return_cny + settled_at + status + 偏差）。
// 仅 status='open' 可结算（调用方校验）。
func (s *Store) SettleRepoTrade(ctx context.Context, id int64, actualReturnCny float64, deviationReturnCny, deviationReturnPct *float64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE cnfi_repo_trades
		SET actual_return_cny = $2, settled_at = now(), status = 'settled',
		    deviation_return_cny = $3, deviation_return_pct = $4, updated_at = now()
		WHERE id = $1 AND status = 'open'`,
		id, actualReturnCny, deviationReturnCny, deviationReturnPct)
	return err
}

// DeleteRepoTrade 删除未结算记录（仅 open 态可删）。
// 返回 (true, nil)=删除成功；(false, nil)=无匹配行（已结算或不存在）；(false, err)=错误。
func (s *Store) DeleteRepoTrade(ctx context.Context, id int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM cnfi_repo_trades WHERE id = $1 AND status = 'open'`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ListMaturedRepoTrades 查询到期未提醒的 open 记录（checkMaturity 用）。
// 条件：status='open' AND maturity_date <= today AND (last_maturity_reminded_at IS NULL OR last_maturity_reminded_at < today)。
func (s *Store) ListMaturedRepoTrades(ctx context.Context, today time.Time) ([]RepoTradeRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, instrument_code, trade_date, maturity_date, holding_days,
		       principal_cny, actual_rate_pct,
		       theoretical_rate_pct, theoretical_return_by_idle, theoretical_return_cny,
		       idle_cny_at_record, signal_snapshot,
		       actual_return_cny, settled_at,
		       deviation_rate_bps, deviation_principal_cny,
		       deviation_return_cny, deviation_return_pct,
		       status, repo_op_id, entry_source, last_maturity_reminded_at,
		       rationale, created_at, updated_at
		FROM cnfi_repo_trades
		WHERE status = 'open' AND maturity_date <= $1
		  AND (last_maturity_reminded_at IS NULL OR last_maturity_reminded_at::date < $1::date)
		ORDER BY maturity_date ASC`, today.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoTradeRow
	for rows.Next() {
		var r RepoTradeRow
		if err := scanRepoTradeRow(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateMaturityReminded 更新到期提醒时间（幂等：每天最多提醒一次）。
func (s *Store) UpdateMaturityReminded(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE cnfi_repo_trades SET last_maturity_reminded_at = now(), updated_at = now() WHERE id = $1`, id)
	return err
}

// RepoStrategyValidation 策略验证总览聚合数据。
type RepoStrategyValidation struct {
	TotalTrades              int32
	SettledTrades            int32
	OpenTrades               int32
	TotalPrincipalCny        float64
	TotalTheoreticalReturn   float64
	TotalActualReturn        float64
	TotalDeviationCny        float64
	TotalDeviationPct        float64
	AvgRateDeviationBps      float64
	AvgPrincipalDeviationCny float64
	OpenPrincipalCny         float64
	OpenTheoreticalReturn    float64
}

// GetRepoStrategyValidation 聚合已结算行 + 在途行统计（后端算好，前端零计算）。
func (s *Store) GetRepoStrategyValidation(ctx context.Context) (*RepoStrategyValidation, error) {
	v := &RepoStrategyValidation{}
	// 已结算行聚合
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(principal_cny), 0),
		       COALESCE(SUM(theoretical_return_cny), 0),
		       COALESCE(SUM(actual_return_cny), 0),
		       COALESCE(SUM(deviation_return_cny), 0),
		       COALESCE(AVG(deviation_rate_bps) FILTER (WHERE deviation_rate_bps IS NOT NULL), 0),
		       COALESCE(AVG(deviation_principal_cny) FILTER (WHERE deviation_principal_cny IS NOT NULL), 0)
		FROM cnfi_repo_trades WHERE status = 'settled'`).Scan(
		&v.SettledTrades, &v.TotalPrincipalCny, &v.TotalTheoreticalReturn,
		&v.TotalActualReturn, &v.TotalDeviationCny,
		&v.AvgRateDeviationBps, &v.AvgPrincipalDeviationCny)
	if err != nil {
		return nil, err
	}
	// 收益偏差率 = 累计偏差 / 累计理论 * 100（除零保护）
	if v.TotalTheoreticalReturn != 0 {
		v.TotalDeviationPct = v.TotalDeviationCny / v.TotalTheoreticalReturn * 100
	}
	// 在途行聚合
	err = s.pool.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(principal_cny), 0), COALESCE(SUM(theoretical_return_cny), 0)
		FROM cnfi_repo_trades WHERE status = 'open'`).Scan(
		&v.OpenTrades, &v.OpenPrincipalCny, &v.OpenTheoreticalReturn)
	if err != nil {
		return nil, err
	}
	v.TotalTrades = v.SettledTrades + v.OpenTrades
	return v, nil
}

// scanRepoTradeRow 从 pgx.Rows 扫描一行到 RepoTradeRow。
func scanRepoTradeRow(rows pgx.Rows, r *RepoTradeRow) error {
	return scanRepoTradeRowScan(rows.Scan, r)
}

// scanRepoTradeRowScan 通用扫描函数（兼容 pgx.Rows 和 pgx.Row）。
func scanRepoTradeRowScan(scan func(dest ...any) error, r *RepoTradeRow) error {
	return scan(
		&r.ID, &r.InstrumentCode, &r.TradeDate, &r.MaturityDate, &r.HoldingDays,
		&r.PrincipalCny, &r.ActualRatePct,
		&r.TheoreticalRatePct, &r.TheoreticalReturnByIdle, &r.TheoreticalReturnCny,
		&r.IdleCnyAtRecord, &r.SignalSnapshot,
		&r.ActualReturnCny, &r.SettledAt,
		&r.DeviationRateBps, &r.DeviationPrincipalCny,
		&r.DeviationReturnCny, &r.DeviationReturnPct,
		&r.Status, &r.RepoOpID, &r.EntrySource, &r.LastMaturityRemindedAt,
		&r.Rationale, &r.CreatedAt, &r.UpdatedAt,
	)
}
