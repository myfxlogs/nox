// D-258 S1：能量租金率序列查询（区间和+区间最早 ts）。
// D-278 S1/S4：净收益率日曲线三成分日序列+累积未对冲收益（rental+vote_claim）。
package pgstore

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// TrxYieldDay D-278 S1：日级三成分聚合（energy/voting TRX + overnight USD + price）。
// energy/voting 来自 fin_trx_txs（rental_income/vote_claim 按 date(ts) 分组）；
// overnight 来自 fin_cmc_history（OVERNIGHT_SETTLEMENT 按 date(transaction_time) 分组）；
// price 来自 position_daily_income.price（rollup 时捕获的当日 TRXUSDT mid，nil=无报价）。
type TrxYieldDay struct {
	Day         time.Time
	EnergyTrx   float64
	VotingTrx   float64
	InterestUSD float64
	Price       *float64 // nil = 无报价
}

// ListTRXYieldByDay D-278 S1：返回 [from, to) 区间日级三成分序列（按 day ASC）。
// 三源分别按日聚合后在 Go 侧 merge by day（避免三表 JOIN 的 NULL 笛卡尔膨胀）。
// 空区间/空表返回 nil（冷启动不猜）。
func (s *Store) ListTRXYieldByDay(ctx context.Context, from, to time.Time) ([]TrxYieldDay, error) {
	from = from.UTC()
	to = to.UTC()
	if !from.Before(to) {
		return nil, nil
	}

	// 1. fin_trx_txs 按 day 聚合 rental_income + vote_claim
	type txRow struct {
		Day     time.Time
		EnergyS int64
		VotingS int64
	}
	txRows, err := s.pool.Query(ctx, `
		SELECT date_trunc('day', ts AT TIME ZONE 'UTC')::timestamptz AS day,
		       COALESCE(SUM(CASE WHEN kind = 'rental_income' THEN amount_sun ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN kind = 'vote_claim' THEN amount_sun ELSE 0 END), 0)
		FROM fin_trx_txs
		WHERE ts >= $1 AND ts < $2
		  AND kind IN ('rental_income', 'vote_claim')
		GROUP BY 1
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("list trx yield tx by day: %w", err)
	}
	defer txRows.Close()
	txByDay := map[time.Time]*txRow{}
	for txRows.Next() {
		var r txRow
		if err := txRows.Scan(&r.Day, &r.EnergyS, &r.VotingS); err != nil {
			return nil, err
		}
		r.Day = r.Day.UTC().Truncate(24 * time.Hour)
		txByDay[r.Day] = &r
	}
	if err := txRows.Err(); err != nil {
		return nil, err
	}

	// 2. fin_cmc_history 按 day 聚合 OVERNIGHT_SETTLEMENT
	overnightRows, err := s.pool.Query(ctx, `
		SELECT date_trunc('day', transaction_time AT TIME ZONE 'UTC')::timestamptz AS day,
		       COALESCE(SUM(amount), 0)
		FROM fin_cmc_history
		WHERE transaction_time >= $1 AND transaction_time < $2
		  AND transaction_type = 'OVERNIGHT_SETTLEMENT'
		GROUP BY 1
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("list trx yield overnight by day: %w", err)
	}
	defer overnightRows.Close()
	overnightByDay := map[time.Time]float64{}
	for overnightRows.Next() {
		var day time.Time
		var sum float64
		if err := overnightRows.Scan(&day, &sum); err != nil {
			return nil, err
		}
		day = day.UTC().Truncate(24 * time.Hour)
		overnightByDay[day] = sum
	}
	if err := overnightRows.Err(); err != nil {
		return nil, err
	}

	// 3. position_daily_income 按 day 取 price（rollup 时捕获）
	priceRows, err := s.pool.Query(ctx, `
		SELECT day, price FROM position_daily_income
		WHERE day >= $1 AND day < $2
	`, from.UTC().Truncate(24*time.Hour), to.UTC().Truncate(24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("list trx yield price by day: %w", err)
	}
	defer priceRows.Close()
	priceByDay := map[time.Time]*float64{}
	for priceRows.Next() {
		var day time.Time
		var price *float64
		if err := priceRows.Scan(&day, &price); err != nil {
			return nil, err
		}
		day = day.UTC().Truncate(24 * time.Hour)
		priceByDay[day] = price
	}
	if err := priceRows.Err(); err != nil {
		return nil, err
	}

	// 4. Merge by day
	daySet := map[time.Time]bool{}
	for d := range txByDay {
		daySet[d] = true
	}
	for d := range overnightByDay {
		daySet[d] = true
	}
	out := make([]TrxYieldDay, 0, len(daySet))
	for d := range daySet {
		yp := TrxYieldDay{Day: d, Price: priceByDay[d]}
		if r, ok := txByDay[d]; ok {
			yp.EnergyTrx = float64(r.EnergyS) / 1e6
			yp.VotingTrx = float64(r.VotingS) / 1e6
		}
		yp.InterestUSD = overnightByDay[d]
		out = append(out, yp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day.Before(out[j].Day) })
	return out, nil
}

// SumTRXTxRentalByRange D-258 S1：返回 [from, to) 区间 rental_income 之和（amount_sun）。
// 仿 SumCMCOvernightByRange 模式；from/to UTC 归一，from>=to 返回 0。
func (s *Store) SumTRXTxRentalByRange(ctx context.Context, from, to time.Time) (int64, error) {
	from = from.UTC()
	to = to.UTC()
	if !from.Before(to) {
		return 0, nil
	}
	var sum int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_sun), 0)
		FROM fin_trx_txs
		WHERE kind = 'rental_income' AND ts >= $1 AND ts < $2
	`, from, to).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum trx rental by range: %w", err)
	}
	return sum, nil
}

// FirstTRXTxRentalTsInRange D-258 S1：返回 [from, to) 区间最早的 rental_income ts。
// 空表/无匹配返回零值+false（供 TrxEnergyRentalAPR 判定实际样本天数）。
func (s *Store) FirstTRXTxRentalTsInRange(ctx context.Context, from, to time.Time) (time.Time, bool, error) {
	from = from.UTC()
	to = to.UTC()
	var ts time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(MIN(ts), '0001-01-01T00:00:00Z'::timestamptz)
		FROM fin_trx_txs
		WHERE kind = 'rental_income' AND ts >= $1 AND ts < $2
	`, from, to).Scan(&ts)
	if err != nil {
		return ts, false, fmt.Errorf("first trx rental ts in range: %w", err)
	}
	if ts.IsZero() {
		return ts, false, nil
	}
	return ts, true, nil
}

// AvgTRXVoteAprByRange D-258 S3：返回 [from, to) 区间 fin_trx_vote_apr.current_rate 均值。
// 空表/无匹配返回 false（冷启动不猜）。
func (s *Store) AvgTRXVoteAprByRange(ctx context.Context, from, to time.Time) (float64, bool, error) {
	from = from.UTC()
	to = to.UTC()
	var avg *float64
	err := s.pool.QueryRow(ctx, `
		SELECT AVG(current_rate) FROM fin_trx_vote_apr
		WHERE fetched_at >= $1 AND fetched_at < $2
	`, from, to).Scan(&avg)
	if err != nil {
		return 0, false, fmt.Errorf("avg trx vote apr by range: %w", err)
	}
	if avg == nil {
		return 0, false, nil
	}
	return *avg, true, nil
}

// EnergyDelegatedRangeInWindow 9-9 过渡期护栏：[from,to) 窗内 fin_trx_account 快照的
// energy_delegated min/max。max>min×1.2（min>0）=扩容/减仓过渡期——7d 窗读数分母失真，
// collector 跳过 floor 评估、雷达 RPC 置 transition 供前端灰显（D-258 尾巴，B' 教训）。
func (s *Store) EnergyDelegatedRangeInWindow(ctx context.Context, from, to time.Time) (int64, int64, error) {
	from = from.UTC()
	to = to.UTC()
	if !from.Before(to) {
		return 0, 0, nil
	}
	var minE, maxE int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(MIN(energy_delegated),0), COALESCE(MAX(energy_delegated),0)
		FROM fin_trx_account
		WHERE fetched_at >= $1 AND fetched_at < $2
	`, from, to).Scan(&minE, &maxE)
	if err != nil {
		return 0, 0, fmt.Errorf("energy delegated range in window: %w", err)
	}
	return minE, maxE, nil
}

// SumTRXTxRentalAll D-278 S4：全量 rental_income 累计（amount_sun）。
// 用于累积未对冲 TRX 收益 = Σ(rental_income + vote_claim)。
func (s *Store) SumTRXTxRentalAll(ctx context.Context) (int64, error) {
	var sum int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_sun), 0) FROM fin_trx_txs WHERE kind = 'rental_income'
	`).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum trx rental all: %w", err)
	}
	return sum, nil
}

// SumTRXTxVoteClaimAll D-278 S4：全量 vote_claim 累计（amount_sun）。
func (s *Store) SumTRXTxVoteClaimAll(ctx context.Context) (int64, error) {
	var sum int64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_sun), 0) FROM fin_trx_txs WHERE kind = 'vote_claim'
	`).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum trx vote claim all: %w", err)
	}
	return sum, nil
}
