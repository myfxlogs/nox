// D-299 PAXG-XAU 跨市场套利监控快照表 CRUD。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// XassetMonitor fin_xasset_monitor 行。
type XassetMonitor struct {
	ID               int64
	Ts               time.Time
	Pair             string
	BinanceBid       float64
	BinanceAsk       float64
	ExnessBid        float64
	ExnessAsk        float64
	EntrySpreadBps   float64
	ExitSpreadBps    float64
	EntryNetBps      float64
	FundingRate8h    float64
	FundingAnnualPct float64
	TrackingErrorBps float64
	EquivAnnualPct   float64
}

// InsertXassetMonitor 插入监控快照，回填 id + ts。
func (s *Store) InsertXassetMonitor(ctx context.Context, r *XassetMonitor) error {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO fin_xasset_monitor (
			pair, binance_bid, binance_ask, exness_bid, exness_ask,
			entry_spread_bps, exit_spread_bps, entry_net_bps,
			funding_rate_8h, funding_annual_pct, tracking_error_bps, equiv_annual_pct
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id, ts
	`, r.Pair, r.BinanceBid, r.BinanceAsk, r.ExnessBid, r.ExnessAsk,
		r.EntrySpreadBps, r.ExitSpreadBps, r.EntryNetBps,
		r.FundingRate8h, r.FundingAnnualPct, r.TrackingErrorBps, r.EquivAnnualPct,
	).Scan(&r.ID, &r.Ts)
	if err != nil {
		return fmt.Errorf("insert xasset monitor: %w", err)
	}
	return nil
}

// LatestXassetMonitor 读最新一行。
func (s *Store) LatestXassetMonitor(ctx context.Context) (*XassetMonitor, error) {
	r := &XassetMonitor{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, ts, pair, binance_bid, binance_ask, exness_bid, exness_ask,
			entry_spread_bps, exit_spread_bps, entry_net_bps,
			funding_rate_8h, funding_annual_pct, tracking_error_bps, equiv_annual_pct
		FROM fin_xasset_monitor ORDER BY ts DESC LIMIT 1
	`).Scan(&r.ID, &r.Ts, &r.Pair, &r.BinanceBid, &r.BinanceAsk, &r.ExnessBid, &r.ExnessAsk,
		&r.EntrySpreadBps, &r.ExitSpreadBps, &r.EntryNetBps,
		&r.FundingRate8h, &r.FundingAnnualPct, &r.TrackingErrorBps, &r.EquivAnnualPct)
	if err != nil {
		return nil, fmt.Errorf("latest xasset monitor: %w", err)
	}
	return r, nil
}
