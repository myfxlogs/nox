// venue_account_snapshot.go —— D-358 S2：CEX/MT 账户余额快照落库（喂 D-356 覆盖率面板）。
// 币种核证结论见 migration 0111 头注：原币混合（MT 原币直传/CEX USDT）——列 _raw+currency。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// VenueAccountSnapshot 账户余额快照行（原币值，currency 由 AccountMonitor 透传）。
type VenueAccountSnapshot struct {
	Venue         string
	VenueType     string
	BalanceRaw    float64
	EquityRaw     float64
	FreeMarginRaw float64
	Currency      string
	FetchedAt     time.Time
}

// InsertVenueAccountSnapshots 批量落账户快照（单 SQL 多行 VALUES；空切片 no-op）。
// append 语义：扫描即周期任务，无需去重（D-358 §S2）。
func (s *Store) InsertVenueAccountSnapshots(ctx context.Context, snaps []VenueAccountSnapshot) error {
	if len(snaps) == 0 {
		return nil
	}
	sql := `INSERT INTO fin_venue_account_snapshot
		(venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at) VALUES `
	args := make([]any, 0, len(snaps)*7)
	for i, sn := range snaps {
		if i > 0 {
			sql += ","
		}
		base := i * 7
		sql += fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7)
		args = append(args, sn.Venue, sn.VenueType, sn.BalanceRaw, sn.EquityRaw,
			sn.FreeMarginRaw, sn.Currency, sn.FetchedAt)
	}
	if _, err := s.pool.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("insert venue account snapshots: %w", err)
	}
	return nil
}
