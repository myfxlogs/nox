// venue_io.go —— D-365 S1：CEX 进出账流水存储（充值/提现，幂等落库+水位游标）。
package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// VenueIO fin_venue_io 行（进出账一条：充值 deposit / 提现 withdraw）。
type VenueIO struct {
	Venue      string
	ConfigID   int64
	Direction  string // deposit / withdraw
	Ccy        string
	Amount     float64
	Fee        *float64
	Status     string // 交易所原生状态码原样存（枚举注释见采集器）
	TxID       string
	ExternalID string // binance.id·tranId / okx depId·wdId·billId
	Ts         time.Time
	// D-539①：内部划转（direction='transfer'）钱包两端——快照 venue_type 键域
	// （spot/funding/earn/perp/unified/margin）；充提行 NULL。
	FromWallet string
	ToWallet   string
}

// InsertVenueIO 批量幂等写入（唯一键 (venue,config_id,direction,external_id) 冲突 DO
// NOTHING）。返回实插行数（幂等重跑=0）。
func (s *Store) InsertVenueIO(ctx context.Context, rows []VenueIO) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(`
			INSERT INTO fin_venue_io (venue, config_id, direction, ccy, amount, fee, status, txid, external_id, ts, from_wallet, to_wallet)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (venue, config_id, direction, external_id) DO NOTHING`,
			r.Venue, r.ConfigID, r.Direction, r.Ccy, r.Amount, r.Fee, r.Status, r.TxID, r.ExternalID, r.Ts,
			r.FromWallet, r.ToWallet)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	var written int64
	for range rows {
		cmd, err := br.Exec()
		if err != nil {
			return written, fmt.Errorf("insert venue io: %w", err)
		}
		written += cmd.RowsAffected()
	}
	return written, nil
}

// LatestVenueIOTs 水位游标：该配置该方向已入账最大事件 ts（表内派生零新状态，重启安全）。
// 空表 → (零值,false,nil)。
func (s *Store) LatestVenueIOTs(ctx context.Context, configID int64, direction string) (time.Time, bool, error) {
	var ts *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT MAX(ts) FROM fin_venue_io WHERE config_id = $1 AND direction = $2`, configID, direction).Scan(&ts)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("latest venue io ts: %w", err)
	}
	if ts == nil {
		return time.Time{}, false, nil
	}
	return *ts, true, nil
}

// RecentVenueIO 财务页流水：该配置最近 N 行（deposit+withdraw 合并，ts 倒序）。
func (s *Store) RecentVenueIO(ctx context.Context, venue string, configID int64, limit int) ([]VenueIO, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT direction, ccy, amount, COALESCE(fee, 0), COALESCE(status, ''), COALESCE(txid, ''), ts
		FROM fin_venue_io
		WHERE venue = $1 AND config_id = $2
		ORDER BY ts DESC LIMIT $3`, venue, configID, limit)
	if err != nil {
		return nil, fmt.Errorf("recent venue io: %w", err)
	}
	defer rows.Close()
	var out []VenueIO
	for rows.Next() {
		var r VenueIO
		if err := rows.Scan(&r.Direction, &r.Ccy, &r.Amount, &r.Fee, &r.Status, &r.TxID, &r.Ts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecentIncomeByConfig 财务页收益流水：该 (venue,venue_type) 通道最近 N 条 income
// （funding fee/earn 利息等实收，D-365 S4 并入账户块）。
func (s *Store) RecentIncomeByConfig(ctx context.Context, venue, venueType string, limit int) ([]IncomeEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT income_type, amount_usd, symbol, ts
		FROM income_events
		WHERE venue = $1 AND venue_type = $2
		ORDER BY ts DESC LIMIT $3`, venue, venueType, limit)
	if err != nil {
		return nil, fmt.Errorf("recent income by config: %w", err)
	}
	defer rows.Close()
	var out []IncomeEvent
	for rows.Next() {
		var e IncomeEvent
		if err := rows.Scan(&e.IncomeType, &e.AmountUSD, &e.Symbol, &e.Ts); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// BalanceSnap D-602：fin_venue_account_snapshot 相邻差分窗（id/balance/ts 三元组）。
type BalanceSnap struct {
	ID      int64
	Balance float64
	Ts      time.Time
}

// BalanceDeltaWindows MT5 账户快照时序窗（balance_raw 相邻差分落账器输入——
// 升序返回；venue=account id 键）。
func (s *Store) BalanceDeltaWindows(ctx context.Context, venue string, since time.Time) ([]BalanceSnap, error) {
	// D-604：差分输入行限事件落地帧（mt_frame=流帧/mt_event=事件锚定写径）——
	// rest 系周期摄取快照（rest_poll/rest_calib/scheduler）禁入差分输入。
	// 小项⑶：id tiebreaker 防 fetched_at 同秒序不稳。
	rows, err := s.pool.Query(ctx, `
		SELECT id, balance_raw, fetched_at FROM fin_venue_account_snapshot
		WHERE venue=$1 AND fetched_at>=$2 AND balance_raw IS NOT NULL
		  AND src IN ('mt_frame','mt_event')
		ORDER BY fetched_at, id`, venue, since)
	if err != nil {
		return nil, fmt.Errorf("balance snaps %s: %w", venue, err)
	}
	defer rows.Close()
	var out []BalanceSnap
	for rows.Next() {
		var b BalanceSnap
		if err := rows.Scan(&b.ID, &b.Balance, &b.Ts); err != nil {
			return nil, fmt.Errorf("scan snap: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ClosedPositionPnLBetween 窗口内关闭持仓的 realized_pnl 合计（差分残差抵扣——
// venue 腿命中的 positions.realized_pnl；PnL 入账≠外部充提）。
func (s *Store) ClosedPositionPnLBetween(ctx context.Context, venue string, from, to time.Time) (float64, error) {
	var sum float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(p.realized_pnl),0) FROM positions p
		WHERE p.status='CLOSED' AND p.closed_at>$2 AND p.closed_at<=$3
		  AND EXISTS (SELECT 1 FROM position_legs l WHERE l.position_id=p.id AND l.venue=$1)`,
		venue, from, to).Scan(&sum)
	return sum, err
}
