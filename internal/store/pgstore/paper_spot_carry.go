// paper_spot_carry.go D-350 阶段②：spot-carry 纸面验证轨存储（开仓/累积/退出三态）。
package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrPaperExists 同 venue+symbol 已有活跃纸面/demo 仓（调用方跳过，不中断扫描轮）。
var ErrPaperExists = errors.New("paper spot carry: active row exists")

// PaperSpotCarry paper_spot_carry 一行（纸面仓或 demo 仓）。
type PaperSpotCarry struct {
	ID              int64
	Venue           string
	Symbol          string
	Side            string // short_perp / long_perp
	EntryFundingAnn float64
	EntryTs         time.Time
	QtyUSD          float64
	LastFundingAnn  *float64
	AccruedUSD      float64
	LastAccrueTs    *time.Time
	LowStreak       int
	ExitTs          *time.Time
	ExitReason      string
	Status          string // active / exited / demo
	Note            string
}

// InsertPaperSpotCarry 开纸面仓（同 venue+symbol 活跃行唯一由部分唯一索引兜底；
// 撞唯一 → 返回 ErrPaperExists 由调用方跳过，不中断本轮）。
func (s *Store) InsertPaperSpotCarry(ctx context.Context, r *PaperSpotCarry) error {
	r.Status = "active"
	err := s.pool.QueryRow(ctx, `
		INSERT INTO paper_spot_carry (venue, symbol, side, entry_funding_ann, qty_usd, last_funding_ann, last_accrue_ts)
		VALUES ($1,$2,$3,$4,$5,$6,now())
		ON CONFLICT (venue, symbol) WHERE status IN ('active','demo') DO NOTHING
		RETURNING id, status`,
		r.Venue, r.Symbol, r.Side, r.EntryFundingAnn, r.QtyUSD, r.EntryFundingAnn).Scan(&r.ID, &r.Status)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ErrPaperExists
		}
		return fmt.Errorf("insert paper spot carry: %w", err)
	}
	return nil
}

// ListActivePaperSpotCarry 活跃纸面仓（status='active'）。
func (s *Store) ListActivePaperSpotCarry(ctx context.Context) ([]PaperSpotCarry, error) {
	return s.scanPaperSpotCarry(ctx, `SELECT `+paperSpotCarryCols+` FROM paper_spot_carry WHERE status = 'active' ORDER BY id`)
}

// ListDemoPaperSpotCarry demo 实测仓（status='demo'，D-350 阶段③）。
func (s *Store) ListDemoPaperSpotCarry(ctx context.Context) ([]PaperSpotCarry, error) {
	return s.scanPaperSpotCarry(ctx, `SELECT `+paperSpotCarryCols+` FROM paper_spot_carry WHERE status = 'demo' ORDER BY id`)
}

// AccruePaperSpotCarry 逐轮累积：accrued += last_funding_ann/100/365×间隔天×qty；回写 last_funding_ann/last_accrue_ts/low_streak。
// 返回更新后的 low_streak 与本轮增量。
func (s *Store) AccruePaperSpotCarry(ctx context.Context, id int64, lastFundingAnn float64, accruedAdd float64, lowStreak int) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE paper_spot_carry
		SET accrued_usd = accrued_usd + $2, last_funding_ann = $3,
		    last_accrue_ts = now(), low_streak = $4
		WHERE id = $1`, id, accruedAdd, lastFundingAnn, lowStreak)
	if err != nil {
		return fmt.Errorf("accrue paper spot carry %d: %w", id, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("accrue paper spot carry %d: row gone", id)
	}
	return nil
}

// MarkPaperSpotCarryDemo 纸面行标记为 demo 实测仓（阶段③：记录双腿回执 note）。
func (s *Store) MarkPaperSpotCarryDemo(ctx context.Context, id int64, note string) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE paper_spot_carry SET status = 'demo', note = $2 WHERE id = $1`, id, note)
	if err != nil {
		return fmt.Errorf("mark paper spot carry demo %d: %w", id, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("mark paper spot carry demo %d: row gone", id)
	}
	return nil
}

// ExitPaperSpotCarry 退出（reason='normalized'/'age_7d'）。
func (s *Store) ExitPaperSpotCarry(ctx context.Context, id int64, reason string) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE paper_spot_carry
		SET status = 'exited', exit_ts = now(), exit_reason = $2
		WHERE id = $1 AND status = 'active'`, id, reason)
	if err != nil {
		return fmt.Errorf("exit paper spot carry %d: %w", id, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("exit paper spot carry %d: not active", id)
	}
	return nil
}

const paperSpotCarryCols = `id, venue, symbol, side, entry_funding_ann, entry_ts, qty_usd,
	last_funding_ann, accrued_usd, last_accrue_ts, low_streak, exit_ts, exit_reason, status, note`

func (s *Store) scanPaperSpotCarry(ctx context.Context, q string) ([]PaperSpotCarry, error) {
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list paper spot carry: %w", err)
	}
	defer rows.Close()
	var out []PaperSpotCarry
	for rows.Next() {
		var r PaperSpotCarry
		if err := rows.Scan(&r.ID, &r.Venue, &r.Symbol, &r.Side, &r.EntryFundingAnn, &r.EntryTs,
			&r.QtyUSD, &r.LastFundingAnn, &r.AccruedUSD, &r.LastAccrueTs, &r.LowStreak,
			&r.ExitTs, &r.ExitReason, &r.Status, &r.Note); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
