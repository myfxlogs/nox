// D-211 TRX 质押监控 P1：fin_trx_* 3 表持久化（SSOT §3）。
// 3 写入 + 3 查询方法，挂在 *Store 上复用 s.pool。
package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------- 结构体 ----------

// TRXAccount 对应 fin_trx_account 表（5min 快照）。
// Unfrozen 为 nil = 无在途解押（指针扫描纪律，D-208 教训）。
type TRXAccount struct {
	ID              int64
	Address         string
	VotesTotal      int64
	TronPower       int64   // 质押量（sun，=bandwidthStaked+energyDelegated）
	SRAddress       *string // nil = 无投票
	BalanceSun      int64
	AllowanceSun    int64
	EnergyDelegated int64
	BandwidthStaked int64
	NetLimit        int64
	Unfrozen        json.RawMessage // nil = 无在途；JSONB 透传
	FetchedAt       time.Time
}

// TRXTx 对应 fin_trx_txs 表（收入交易流，幂等键 tx_id）。
// AmountSun=0 用于 vote_claim（WithdrawBalanceContract 无金额参数，P2-2）。
type TRXTx struct {
	ID           int64
	TxID         string
	Ts           time.Time
	Kind         string // rental_income|vote_claim|deposit|withdraw|stake|unstake|other
	AmountSun    int64
	Counterparty *string // nil = 无对手方
	FetchedAt    time.Time
}

// TRXWitness 对应 fin_trx_witness 表（慢变，24h）。
type TRXWitness struct {
	ID        int64
	SRAddress string
	URL       *string // nil = listwitnesses 无 url
	VoteCount *int64  // nil = 未返回
	FetchedAt time.Time
}

// ---------- 写入方法 ----------

// InsertTRXAccount 写一条账户快照（5min 级，append-only）。
func (s *Store) InsertTRXAccount(ctx context.Context, a TRXAccount) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_trx_account
		  (address, votes_total, tron_power, sr_address, balance_sun, allowance_sun,
		   energy_delegated, bandwidth_staked, net_limit, unfrozen, fetched_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,COALESCE($11,now()))
	`, a.Address, a.VotesTotal, a.TronPower, a.SRAddress, a.BalanceSun, a.AllowanceSun,
		a.EnergyDelegated, a.BandwidthStaked, a.NetLimit, a.Unfrozen, a.FetchedAt)
	if err != nil {
		return fmt.Errorf("insert trx account: %w", err)
	}
	return nil
}

// UpsertTRXTx 幂等写入交易流（tx_id UNIQUE 去重）。
func (s *Store) UpsertTRXTx(ctx context.Context, t TRXTx) error {
	if t.TxID == "" {
		return errors.New("trx txs: empty tx_id — field mapping broken (R3 防御：空键坍缩成一行)")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_trx_txs (tx_id, ts, kind, amount_sun, counterparty, fetched_at)
		VALUES ($1,$2,$3,$4,$5,COALESCE($6,now()))
		ON CONFLICT (tx_id) DO NOTHING
	`, t.TxID, t.Ts, t.Kind, t.AmountSun, t.Counterparty, t.FetchedAt)
	if err != nil {
		return fmt.Errorf("upsert trx tx: %w", err)
	}
	return nil
}

// InsertTRXWitness 写一条见证人快照。
func (s *Store) InsertTRXWitness(ctx context.Context, w TRXWitness) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_trx_witness (sr_address, url, vote_count, fetched_at)
		VALUES ($1,$2,$3,COALESCE($4,now()))
	`, w.SRAddress, w.URL, w.VoteCount, w.FetchedAt)
	if err != nil {
		return fmt.Errorf("insert trx witness: %w", err)
	}
	return nil
}

// ---------- 查询方法 ----------

// LastTRXAccount 返回最新一条账户快照（按 fetched_at DESC）。
func (s *Store) LastTRXAccount(ctx context.Context) (TRXAccount, error) {
	var a TRXAccount
	err := s.pool.QueryRow(ctx, `
		SELECT id, address, votes_total, tron_power, sr_address, balance_sun, allowance_sun,
		       energy_delegated, bandwidth_staked, net_limit, unfrozen, fetched_at
		FROM fin_trx_account
		ORDER BY fetched_at DESC
		LIMIT 1
	`).Scan(&a.ID, &a.Address, &a.VotesTotal, &a.TronPower, &a.SRAddress, &a.BalanceSun, &a.AllowanceSun,
		&a.EnergyDelegated, &a.BandwidthStaked, &a.NetLimit, &a.Unfrozen, &a.FetchedAt)
	if err != nil {
		return a, fmt.Errorf("last trx account: %w", err)
	}
	return a, nil
}

// ListTRXAccountByDay 返回指定 UTC 日 [00:00, 23:59] 的账户快照（rollup 用，按 fetched_at ASC）。
func (s *Store) ListTRXAccountByDay(ctx context.Context, day time.Time) ([]TRXAccount, error) {
	start := day.UTC().Truncate(24 * time.Hour)
	end := start.Add(24 * time.Hour)
	rows, err := s.pool.Query(ctx, `
		SELECT id, address, votes_total, tron_power, sr_address, balance_sun, allowance_sun,
		       energy_delegated, bandwidth_staked, net_limit, unfrozen, fetched_at
		FROM fin_trx_account
		WHERE fetched_at >= $1 AND fetched_at < $2
		ORDER BY fetched_at ASC
	`, start, end)
	if err != nil {
		return nil, fmt.Errorf("list trx account by day: %w", err)
	}
	defer rows.Close()
	var out []TRXAccount
	for rows.Next() {
		var a TRXAccount
		if err := rows.Scan(&a.ID, &a.Address, &a.VotesTotal, &a.TronPower, &a.SRAddress, &a.BalanceSun,
			&a.AllowanceSun, &a.EnergyDelegated, &a.BandwidthStaked, &a.NetLimit,
			&a.Unfrozen, &a.FetchedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListTRXTxs 返回最近 limit 条交易流（按 ts DESC）。
func (s *Store) ListTRXTxs(ctx context.Context, limit int) ([]TRXTx, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, tx_id, ts, kind, amount_sun, counterparty, fetched_at
		FROM fin_trx_txs
		ORDER BY ts DESC, id DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list trx txs: %w", err)
	}
	defer rows.Close()
	var out []TRXTx
	for rows.Next() {
		var t TRXTx
		if err := rows.Scan(&t.ID, &t.TxID, &t.Ts, &t.Kind, &t.AmountSun,
			&t.Counterparty, &t.FetchedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SumTRXTxRentalByDay 返回指定 UTC 日 rental_income 之和（amount_sun）。
func (s *Store) SumTRXTxRentalByDay(ctx context.Context, day time.Time) (int64, error) {
	start := day.UTC().Truncate(24 * time.Hour)
	end := start.Add(24 * time.Hour)
	var sum int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_sun), 0)
		FROM fin_trx_txs
		WHERE kind = 'rental_income' AND ts >= $1 AND ts < $2
	`, start, end).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum trx rental by day: %w", err)
	}
	return sum, nil
}

// LastTRXTxTs 返回最新一条交易的 ts（用于增量翻页起点）；空表返回零值+false。
func (s *Store) LastTRXTxTs(ctx context.Context) (time.Time, bool, error) {
	var ts time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT MAX(ts) FROM fin_trx_txs
	`).Scan(&ts)
	if err != nil {
		return ts, false, fmt.Errorf("last trx tx ts: %w", err)
	}
	if ts.IsZero() {
		return ts, false, nil
	}
	return ts, true, nil
}

// FirstTRXTxTs 返回最旧一条交易的 ts（D-211 R5 缺口检测：oldest 远晚于账户创建时间
// 说明 429 断点后 lastTs 推进致早期段缺，需 min_timestamp=0 强制全史回填）；空表返回零值+false。
func (s *Store) FirstTRXTxTs(ctx context.Context) (time.Time, bool, error) {
	var ts time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT MIN(ts) FROM fin_trx_txs
	`).Scan(&ts)
	if err != nil {
		return ts, false, fmt.Errorf("first trx tx ts: %w", err)
	}
	if ts.IsZero() {
		return ts, false, nil
	}
	return ts, true, nil
}

// ListTRXWitnesses 返回最近 limit 条见证人快照（按 fetched_at DESC）。
func (s *Store) ListTRXWitnesses(ctx context.Context, limit int) ([]TRXWitness, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, sr_address, url, vote_count, fetched_at
		FROM fin_trx_witness
		ORDER BY fetched_at DESC, id DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list trx witnesses: %w", err)
	}
	defer rows.Close()
	var out []TRXWitness
	for rows.Next() {
		var w TRXWitness
		if err := rows.Scan(&w.ID, &w.SRAddress, &w.URL, &w.VoteCount, &w.FetchedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// 确保 pgx import 被引用（CopyFrom 批量写入用，S2 采集器可能用）。
var _ = pgx.Identifier{}

// ---------- D-231 TRX 投票收益率（fin_trx_vote_apr） ----------

// TRXVoteApr 对应 fin_trx_vote_apr 表（5min 聚合一行）。
type TRXVoteApr struct {
	ID          int64
	FetchedAt   time.Time
	CurrentRate float64 // 票数加权平均年化 %
	CurrentSr   *string // 当前投票主 SR 名（票数最多者）；nil=无投票
	MaxRate     float64 // 市场最高年化 %（producer=true 降序第一）
	MaxSr       *string // 最高年化 SR 名；nil=无 producer
	Spread      float64 // max_rate − current_rate（pp）
}

// InsertTRXVoteApr 写一条投票收益率聚合行（5min 级，append-only）。
func (s *Store) InsertTRXVoteApr(ctx context.Context, a TRXVoteApr) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_trx_vote_apr
		  (fetched_at, current_rate, current_sr, max_rate, max_sr, spread)
		VALUES (COALESCE($1,now()),$2,$3,$4,$5,$6)
	`, a.FetchedAt, a.CurrentRate, a.CurrentSr, a.MaxRate, a.MaxSr, a.Spread)
	if err != nil {
		return fmt.Errorf("insert trx vote apr: %w", err)
	}
	return nil
}

// LastTRXVoteApr 返回最新一条投票收益率聚合行（按 fetched_at DESC）；空表返回 ErrNoRows。
func (s *Store) LastTRXVoteApr(ctx context.Context) (TRXVoteApr, error) {
	var a TRXVoteApr
	err := s.pool.QueryRow(ctx, `
		SELECT id, fetched_at, current_rate, current_sr, max_rate, max_sr, spread
		FROM fin_trx_vote_apr
		ORDER BY fetched_at DESC
		LIMIT 1
	`).Scan(&a.ID, &a.FetchedAt, &a.CurrentRate, &a.CurrentSr, &a.MaxRate, &a.MaxSr, &a.Spread)
	if err != nil {
		return a, fmt.Errorf("last trx vote apr: %w", err)
	}
	return a, nil
}
