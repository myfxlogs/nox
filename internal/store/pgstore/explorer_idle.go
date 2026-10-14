// explorer_idle.go —— ⑤资金不眠异常读面（自 explorer.go 拆出保行数硬闸）：
// fin_venue_account_snapshot 每 venue 最新行 + 近30d 收入 + 闲置/破地板标记
// + 幽灵池打标（broker_accounts 移除户残行——剔 NAV/不告警）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// ExplorerIdlePool ⑤单 venue 池：最新快照余额 + 近30d收入 + 闲置/破地板标记。
type ExplorerIdlePool struct {
	Venue          string
	VenueType      string
	Balance        float64
	Equity         float64
	FreeMargin     float64
	Currency       string
	BufferMultiple float64 // 快照行直取，NULL → 0
	Income30dUSD   float64 // income_events 近30d 合计，venue_key 粒度归因
	FetchedAt      time.Time
	Idle           bool // 余额>0 且 30d 收入=0（睡钱标记，数字口径确定性）
	BelowFloor     bool // BufferMultiple 有值且 <1（破地板）
	// Ghost 幽灵池（STATE NAV 幽灵池过滤票）：venue_type=mt4/mt5 快照行但 venue
	// 已不在 broker_accounts（账户移除后残行——NAV 虚计；stopped 户仍在册不受影响）。
	// 写侧不动（快照保留=历史真值）；读面打标由 SumNav/展示层剔计。
	Ghost bool
	// D-516 共享持有归属：Balance/Equity/FreeMargin 已乘 Share=归属份；
	// Custodial=代管余量（equity_raw×(1−share)——展示标用，不进判据）。
	Share     float64
	Custodial float64
}

// ExplorerIdleFunds ⑤资金不眠异常：fin_venue_account_snapshot 每 venue 最新行 +
// income_events 近30d 合计 + 快照 buffer_multiple，闲置/破地板标记 Go 算。
func (s *Store) ExplorerIdleFunds(ctx context.Context, now time.Time) ([]ExplorerIdlePool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (venue, venue_type, currency)
			venue, venue_type, COALESCE(balance_raw,0), COALESCE(equity_raw,0),
			COALESCE(free_margin_raw,0), COALESCE(currency,''), fetched_at, COALESCE(buffer_multiple,0)
		FROM fin_venue_account_snapshot
		ORDER BY venue, venue_type, currency, fetched_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("explorer idle snapshots: %w", err)
	}
	defer rows.Close()
	var pools []ExplorerIdlePool
	for rows.Next() {
		var p ExplorerIdlePool
		if err := rows.Scan(&p.Venue, &p.VenueType, &p.Balance, &p.Equity,
			&p.FreeMargin, &p.Currency, &p.FetchedAt, &p.BufferMultiple); err != nil {
			return nil, fmt.Errorf("scan idle snapshot: %w", err)
		}
		pools = append(pools, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// '' 币残影行并入主币行（同池双计防稀释——敞口闸分母口径修）。
	pools = collapseBlankCcyPool(pools,
		func(p ExplorerIdlePool) (string, string, string) { return p.Venue, p.VenueType, p.Currency },
		func(p ExplorerIdlePool) time.Time { return p.FetchedAt },
		func(dst *ExplorerIdlePool, src ExplorerIdlePool) {
			dst.Balance, dst.Equity, dst.FreeMargin, dst.FetchedAt, dst.BufferMultiple =
				src.Balance, src.Equity, src.FreeMargin, src.FetchedAt, src.BufferMultiple
		})
	// 近30d收入（venue_key 归一键优先，裸 venue 兼容——D-496 双键惯例）。
	// 幽灵池过滤：broker 户（mt4/mt5）须在册——移除户残行打标不入 NAV。
	registered := map[string]bool{}
	{
		vrows, verr := s.pool.Query(ctx, `SELECT venue FROM broker_accounts`)
		if verr != nil {
			return nil, fmt.Errorf("explorer idle broker venues: %w", verr)
		}
		for vrows.Next() {
			var v string
			if err := vrows.Scan(&v); err != nil {
				vrows.Close()
				return nil, fmt.Errorf("scan broker venue: %w", err)
			}
			registered[v] = true
		}
		vrows.Close()
		if err := vrows.Err(); err != nil {
			return nil, err
		}
	}
	// 收入判据 venue_key 粒度（okx idle 粒度修票）：仓位 income（如 okx:perp
	// funding）不得掩盖同所 unified 躺账现金；venue_type='' 裸键行=不可归因
	// 收入，并入该 venue 各池保守判闲（宁可不闲不漏睡钱侧——裸键旧语义保）。
	income := map[string]float64{}
	irows, err := s.pool.Query(ctx, `
		SELECT venue_key, COALESCE(SUM(amount_usd),0) FROM income_events
		WHERE ts >= $1 GROUP BY venue_key`, now.Add(-30*24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("explorer idle income: %w", err)
	}
	for irows.Next() {
		var v string
		var sum float64
		if err := irows.Scan(&v, &sum); err != nil {
			irows.Close()
			return nil, fmt.Errorf("scan idle income: %w", err)
		}
		income[v] = sum
	}
	irows.Close()
	if err := irows.Err(); err != nil {
		return nil, err
	}
	shares, serr := s.OwnershipShares(ctx)
	if serr != nil {
		return nil, serr // D-516：系数读败 fail-closed——不静默按全归属放大 NAV
	}
	for i := range pools {
		p := &pools[i]
		if p.VenueType == "mt4" || p.VenueType == "mt5" {
			p.Ghost = !registered[p.Venue]
		}
		p.Share = 1.0
		if f, ok := OwnershipShare(shares, p.Venue, p.VenueType, p.Currency); ok {
			p.Share = f
			p.Custodial = p.Equity * (1 - f)
			p.Balance *= f
			p.Equity *= f
			p.FreeMargin *= f
		}
		p.Income30dUSD = income[p.Venue+":"+p.VenueType] + income[p.Venue]
		if p.Ghost {
			continue // 幽灵池不标 Idle/BelowFloor——移除户残行不告警不虚计
		}
		p.BelowFloor = p.BufferMultiple > 0 && p.BufferMultiple < 1
		p.Idle = p.Balance > 0 && p.Income30dUSD == 0
	}
	return pools, nil
}
