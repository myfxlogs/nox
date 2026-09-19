package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CexUniverseRow 是 fin_cex_universe 一行（D-307 universe 现状表）。
// LaunchTS/FundingAnn/Vol24h/LastAlertTS/CtVal 为 NULL 可空列，用指针表达。
type CexUniverseRow struct {
	Venue, Symbol, Base, Class, Status string
	LaunchTS                           *time.Time
	FirstSeen                          time.Time
	LastAlertTS                        *time.Time
	HedgeCanonical, Tier               string
	FundingAnn, Vol24h, LastBasisBps   *float64
	CtVal                              *float64 // D-311：合约面值（1 张=基础资产数），nil=未知
}

// CexEvent 是 fin_cex_events 一行（append-only 事件）。
type CexEvent struct {
	Venue, Symbol, EventType, Detail string
}

// CexRadarRow 是 fin_cex_radar 一行（L0 全宇宙快照）。
type CexRadarRow struct {
	Venue, Symbol                   string
	FundingAnn, Vol24h, Mark, Index *float64
	HedgeCanonical, Tier            string
}

// ListCexUniverse 返回 universe 现状表全量，key=venue+"|"+symbol。
func (s *Store) ListCexUniverse(ctx context.Context) (map[string]CexUniverseRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT venue, symbol, base, underlying_class, status, launch_ts,
		       first_seen, last_alert_ts, hedge_canonical, tier, last_funding_ann, last_vol24h, ct_val,
		       last_basis_bps
		FROM fin_cex_universe`)
	if err != nil {
		return nil, fmt.Errorf("list cex_universe: %w", err)
	}
	defer rows.Close()
	out := make(map[string]CexUniverseRow)
	for rows.Next() {
		var r CexUniverseRow
		if err := rows.Scan(&r.Venue, &r.Symbol, &r.Base, &r.Class, &r.Status, &r.LaunchTS,
			&r.FirstSeen, &r.LastAlertTS, &r.HedgeCanonical, &r.Tier, &r.FundingAnn, &r.Vol24h, &r.CtVal,
			&r.LastBasisBps); err != nil {
			return nil, err
		}
		out[r.Venue+"|"+r.Symbol] = r
	}
	return out, rows.Err()
}

// UpsertCexUniverse 单事务批量 upsert：ON CONFLICT 更新 status/last_seen/hedge/tier/
// funding/vol 等可变列，不动 first_seen/last_alert_ts。
func (s *Store) UpsertCexUniverse(ctx context.Context, rows []CexUniverseRow, now time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("upsert cex_universe begin: %w", err)
	}
	defer tx.Rollback(ctx) // commit 后 no-op
	for _, r := range rows {
		if _, err := tx.Exec(ctx, `
			INSERT INTO fin_cex_universe (venue, symbol, base, underlying_class, status,
				launch_ts, last_seen, hedge_canonical, tier, last_funding_ann, last_vol24h, ct_val, last_basis_bps)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			ON CONFLICT (venue, symbol) DO UPDATE SET
				base = EXCLUDED.base,
				underlying_class = EXCLUDED.underlying_class,
				status = EXCLUDED.status,
				launch_ts = EXCLUDED.launch_ts,
				last_seen = EXCLUDED.last_seen,
				hedge_canonical = EXCLUDED.hedge_canonical,
				tier = EXCLUDED.tier,
				last_funding_ann = EXCLUDED.last_funding_ann,
				last_vol24h = EXCLUDED.last_vol24h,
				ct_val = EXCLUDED.ct_val,
				last_basis_bps = EXCLUDED.last_basis_bps`,
			r.Venue, r.Symbol, r.Base, r.Class, r.Status,
			r.LaunchTS, now, r.HedgeCanonical, r.Tier, r.FundingAnn, r.Vol24h, r.CtVal,
			r.LastBasisBps); err != nil {
			return fmt.Errorf("upsert cex_universe %s|%s: %w", r.Venue, r.Symbol, err)
		}
	}
	return tx.Commit(ctx)
}

// GetCexContractSize D-311 S3：按 (venue, symbol) 查 fin_cex_universe.ct_val。
// venue=bare 名（gate/okx/binance）、symbol=venue 原生合约名（CL_USDT/BTC-USDT-SWAP/XAUUSDT）。
// 缺行/NULL/≤0 → ok=false（legContractSizeOf 上游 fail-closed 报错，不猜合约规格）。
func (s *Store) GetCexContractSize(ctx context.Context, venue, symbol string) (float64, bool, error) {
	var ctVal *float64
	err := s.pool.QueryRow(ctx,
		`SELECT ct_val FROM fin_cex_universe WHERE venue = $1 AND symbol = $2`,
		venue, symbol).Scan(&ctVal)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("cex contract size %s|%s: %w", venue, symbol, err)
	}
	if ctVal == nil || *ctVal <= 0 {
		return 0, false, nil
	}
	return *ctVal, true, nil
}

// InsertCexEvents 批量落事件行（ts 取库 now()）。
func (s *Store) InsertCexEvents(ctx context.Context, evs []CexEvent) error {
	for _, e := range evs {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO fin_cex_events (venue, symbol, event_type, detail) VALUES ($1,$2,$3,$4)`,
			e.Venue, e.Symbol, e.EventType, e.Detail); err != nil {
			return fmt.Errorf("insert cex_event %s %s: %w", e.EventType, e.Symbol, err)
		}
	}
	return nil
}

// LastCexEventTS 返回 (event_type, symbol) 最近一次事件 ts；ok=false=无记录（冷却判定用）。
func (s *Store) LastCexEventTS(ctx context.Context, eventType, symbol string) (time.Time, bool, error) {
	var ts *time.Time
	if err := s.pool.QueryRow(ctx,
		`SELECT max(ts) FROM fin_cex_events WHERE event_type = $1 AND symbol = $2`,
		eventType, symbol).Scan(&ts); err != nil {
		return time.Time{}, false, fmt.Errorf("last cex_event ts: %w", err)
	}
	if ts == nil {
		return time.Time{}, false, nil
	}
	return *ts, true, nil
}

// InsertCexRadar 批量落 L0 全宇宙快照行（now=轮次 ts，与 universe last_seen 同源）。
func (s *Store) InsertCexRadar(ctx context.Context, rows []CexRadarRow, now time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	for _, r := range rows {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO fin_cex_radar (ts, venue, symbol, funding_ann, vol24h, mark, idx,
				hedge_canonical, tier) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			now, r.Venue, r.Symbol, r.FundingAnn, r.Vol24h, r.Mark, r.Index,
			r.HedgeCanonical, r.Tier); err != nil {
			return fmt.Errorf("insert cex_radar %s|%s: %w", r.Venue, r.Symbol, err)
		}
	}
	return nil
}

// PruneCexRadar 滚动清理：删除 ts 早于 keepDays 前的快照行。
func (s *Store) PruneCexRadar(ctx context.Context, keepDays int) error {
	cutoff := time.Now().Add(-time.Duration(keepDays) * 24 * time.Hour)
	if _, err := s.pool.Exec(ctx, `DELETE FROM fin_cex_radar WHERE ts < $1`, cutoff); err != nil {
		return fmt.Errorf("prune cex_radar: %w", err)
	}
	return nil
}

// TouchCexUniverseAlert 写 last_alert_ts（告警冷却用）。
func (s *Store) TouchCexUniverseAlert(ctx context.Context, venue, symbol string, ts time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE fin_cex_universe SET last_alert_ts = $3 WHERE venue = $1 AND symbol = $2`,
		venue, symbol, ts)
	if err != nil {
		return fmt.Errorf("touch cex_universe alert %s|%s: %w", venue, symbol, err)
	}
	return nil
}
