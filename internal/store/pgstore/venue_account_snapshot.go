// venue_account_snapshot.go —— D-358 S2：CEX/MT 账户余额快照落库（喂 D-356 覆盖率面板）。
// 币种核证结论见 migration 0111 头注：原币混合（MT 原币直传/CEX USDT）——列 _raw+currency。
// buffer_multiple 列（CEX 距强平缓冲倍数=equity/MMR，写时归一；NULL=未采）；
// snapHook 写口钩子（D-405 S3 marginGuard 搭车点：三写口收敛单钩，同步调用由 guard 内部异步化）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// VenueAccountSnapshot 账户余额快照行（原币值，currency 由 AccountMonitor 透传）。
// Src D-383 B1/B2：写路标记（scheduler=调度回放默认/mt_event=MT order-event/okx_event=
// OKX private WS push）；空值落库时回退 scheduler（列默认同值，migration 0121）。
type VenueAccountSnapshot struct {
	Venue          string
	VenueType      string
	BalanceRaw     float64
	EquityRaw      float64
	FreeMarginRaw  float64
	Currency       string
	FetchedAt      time.Time
	Src            string
	BufferMultiple *float64 // D-405：CEX equity/MMR 缓冲倍数；nil=未采（MT/spot 恒 nil）
}

// SetVenueSnapshotHook 注册 snapshot 写口钩子（D-405 S3 marginGuard 搭车点，
// main 装配期调用一次；nil=未接线 no-op）。同步调用——guard 内部必须自行异步化，
// 勿在本函数内阻塞写口。
func (s *Store) SetVenueSnapshotHook(fn func(ctx context.Context, snaps []VenueAccountSnapshot)) {
	s.snapHook = fn
}

// InsertVenueAccountSnapshots 批量落账户快照（单 SQL 多行 VALUES；空切片 no-op）。
// append 语义：扫描即周期任务，无需去重（D-358 §S2）。
// 成功落库后同步触发 snapHook（marginGuard 事件驱动评估挂点，
// 覆盖 scheduler/mt_event/okx_event 三写口）。
func (s *Store) InsertVenueAccountSnapshots(ctx context.Context, snaps []VenueAccountSnapshot) error {
	if len(snaps) == 0 {
		return nil
	}
	sql := `INSERT INTO fin_venue_account_snapshot
		(venue, venue_type, balance_raw, equity_raw, free_margin_raw, currency, fetched_at, src, buffer_multiple) VALUES `
	args := make([]any, 0, len(snaps)*9)
	for i, sn := range snaps {
		if i > 0 {
			sql += ","
		}
		base := i * 9
		sql += fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9)
		src := sn.Src
		if src == "" {
			src = "scheduler" // 既有调度路径零改动（migration 0121 列默认同值）
		}
		args = append(args, sn.Venue, sn.VenueType, sn.BalanceRaw, sn.EquityRaw,
			sn.FreeMarginRaw, sn.Currency, sn.FetchedAt, src, sn.BufferMultiple)
	}
	if _, err := s.pool.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("insert venue account snapshots: %w", err)
	}
	if s.snapHook != nil {
		s.snapHook(ctx, snaps)
	}
	return nil
}

// VenueAccountLatest 每 venue 最新快照（D-359 覆盖率面板行）。
// 补 balance/free_margin/buffer 三列（marginGuard + ListAccountMarginStatus 同源消费）。
type VenueAccountLatest struct {
	Venue          string
	VenueType      string
	BalanceRaw     *float64 // 列可空
	EquityRaw      *float64
	FreeMarginRaw  *float64
	BufferMultiple *float64
	Currency       string
	FetchedAt      time.Time
}

// LatestVenueAccountSnapshots 每 (venue, venue_type, currency) 最新一行（DISTINCT ON 三键，
// 同 venue 双 type 如 okx spot/perp 是两个独立资金池，折叠成一行=漏行；
// currency 入键=binance:earn 分币持仓行（D-285 零换算）不互塌）。
// 空表 → 空 nil 切片（调用方回退「未采」占位）。
func (s *Store) LatestVenueAccountSnapshots(ctx context.Context) ([]VenueAccountLatest, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (venue, venue_type, currency) venue, venue_type, balance_raw, equity_raw,
			free_margin_raw, buffer_multiple, currency, fetched_at
		FROM fin_venue_account_snapshot
		ORDER BY venue, venue_type, currency, fetched_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("latest venue account snapshots: %w", err)
	}
	defer rows.Close()
	var out []VenueAccountLatest
	for rows.Next() {
		var r VenueAccountLatest
		if err := rows.Scan(&r.Venue, &r.VenueType, &r.BalanceRaw, &r.EquityRaw,
			&r.FreeMarginRaw, &r.BufferMultiple, &r.Currency, &r.FetchedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
