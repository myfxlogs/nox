// D-270 逆回购周四操作闭环 Store 方法（S1）。
// cnfi_repo_ops 表管提醒→确认生命周期；cnfi_actions 管实际记账。
// 幂等：Confirm 用 UPDATE WHERE feishu_event_id IS NULL（DB 层原子）。
package pgstore

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// RepoOpRow 是 cnfi_repo_ops 表的一行（D-270）。
type RepoOpRow struct {
	ID                int64
	OpportunityWeek   string
	InstrumentCode    string
	InstrumentName    string
	RatePctAtTrigger  float64
	ExpectedReturnCny *float64
	HoldingDays       int
	SignalSnapshot    *string
	TriggerDate       time.Time
	ReminderCount     int
	LastReminderAt    *time.Time
	Status            string
	OperatedAmountCny *float64
	OperatedRatePct   *float64
	OperatedDate      *time.Time
	SkipReason        *string
	ConfirmedAt       *time.Time
	ConfirmedVia      *string
	FeishuEventID     *string
	FeishuMessageID   *string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// CreateRepoOp 创建本周操作机会（提醒触发时调用）。
// opportunity_week 唯一约束防重复——已存在返回 error。
func (s *Store) CreateRepoOp(ctx context.Context, week, code, name string, ratePct, expectedReturn float64, holdingDays int, signalSnapshot string, triggerDate time.Time) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO cnfi_repo_ops (opportunity_week, instrument_code, instrument_name,
		                           rate_pct_at_trigger, expected_return_cny, holding_days,
		                           signal_snapshot, trigger_date, reminder_count, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, 'pending')
		RETURNING id`,
		week, code, name, ratePct, expectedReturn, holdingDays, signalSnapshot,
		triggerDate.Format("2006-01-02")).Scan(&id)
	return id, err
}

// GetPendingRepoOp 取本周待确认的操作机会（提醒循环+确认处理用）。
// 无记录返回 (nil, nil)（本周无提醒或已确认）。
func (s *Store) GetPendingRepoOp(ctx context.Context, week string) (*RepoOpRow, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, opportunity_week, instrument_code, instrument_name,
		       rate_pct_at_trigger, expected_return_cny, holding_days, signal_snapshot,
		       trigger_date, reminder_count, last_reminder_at, status,
		       operated_amount_cny, operated_rate_pct, operated_date, skip_reason,
		       confirmed_at, confirmed_via, feishu_event_id, feishu_message_id,
		       created_at, updated_at
		FROM cnfi_repo_ops
		WHERE opportunity_week = $1 AND status = 'pending'
		ORDER BY id DESC LIMIT 1`, week)
	var r RepoOpRow
	err := row.Scan(&r.ID, &r.OpportunityWeek, &r.InstrumentCode, &r.InstrumentName,
		&r.RatePctAtTrigger, &r.ExpectedReturnCny, &r.HoldingDays, &r.SignalSnapshot,
		&r.TriggerDate, &r.ReminderCount, &r.LastReminderAt, &r.Status,
		&r.OperatedAmountCny, &r.OperatedRatePct, &r.OperatedDate, &r.SkipReason,
		&r.ConfirmedAt, &r.ConfirmedVia, &r.FeishuEventID, &r.FeishuMessageID,
		&r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// IncrementRepoReminder 更新提醒计数+时间（再次提醒时调用）。
func (s *Store) IncrementRepoReminder(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE cnfi_repo_ops
		SET reminder_count = reminder_count + 1, last_reminder_at = now(), updated_at = now()
		WHERE id = $1`, id)
	return err
}

// ConfirmRepoOpOperated 确认操作（DB 层原子幂等：UPDATE WHERE feishu_event_id IS NULL）。
// 返回 (true, nil)=确认成功；(false, nil)=已被另一并发处理（幂等）；(false, err)=错误。
func (s *Store) ConfirmRepoOpOperated(ctx context.Context, id int64, amountCny, ratePct float64, operatedDate time.Time, eventID, messageID, via string) (bool, error) {
	var updatedID int64
	err := s.pool.QueryRow(ctx, `
		UPDATE cnfi_repo_ops
		SET status = 'operated', operated_amount_cny = $2, operated_rate_pct = $3,
		    operated_date = $4, confirmed_at = now(), confirmed_via = $5,
		    feishu_event_id = $6, feishu_message_id = $7, updated_at = now()
		WHERE id = $1 AND feishu_event_id IS NULL
		RETURNING id`,
		id, amountCny, ratePct, operatedDate.Format("2006-01-02"), via, eventID, messageID).Scan(&updatedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // 已被另一并发处理（幂等）
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ConfirmRepoOpSkipped 确认跳过（DB 层原子幂等：UPDATE WHERE feishu_event_id IS NULL）。
// 返回 (true, nil)=确认成功；(false, nil)=已被另一并发处理（幂等）；(false, err)=错误。
func (s *Store) ConfirmRepoOpSkipped(ctx context.Context, id int64, reason, eventID, messageID, via string) (bool, error) {
	var updatedID int64
	err := s.pool.QueryRow(ctx, `
		UPDATE cnfi_repo_ops
		SET status = 'skipped', skip_reason = $2, confirmed_at = now(), confirmed_via = $3,
		    feishu_event_id = $4, feishu_message_id = $5, updated_at = now()
		WHERE id = $1 AND feishu_event_id IS NULL
		RETURNING id`,
		id, reason, via, eventID, messageID).Scan(&updatedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // 已被另一并发处理（幂等）
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListRepoOps 列出操作历史（前端展示）。
func (s *Store) ListRepoOps(ctx context.Context, limit int) ([]RepoOpRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, opportunity_week, instrument_code, instrument_name,
		       rate_pct_at_trigger, expected_return_cny, holding_days, signal_snapshot,
		       trigger_date, reminder_count, last_reminder_at, status,
		       operated_amount_cny, operated_rate_pct, operated_date, skip_reason,
		       confirmed_at, confirmed_via, feishu_event_id, feishu_message_id,
		       created_at, updated_at
		FROM cnfi_repo_ops ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoOpRow
	for rows.Next() {
		var r RepoOpRow
		if err := rows.Scan(&r.ID, &r.OpportunityWeek, &r.InstrumentCode, &r.InstrumentName,
			&r.RatePctAtTrigger, &r.ExpectedReturnCny, &r.HoldingDays, &r.SignalSnapshot,
			&r.TriggerDate, &r.ReminderCount, &r.LastReminderAt, &r.Status,
			&r.OperatedAmountCny, &r.OperatedRatePct, &r.OperatedDate, &r.SkipReason,
			&r.ConfirmedAt, &r.ConfirmedVia, &r.FeishuEventID, &r.FeishuMessageID,
			&r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
