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
	// Stale 陈值池标（总览瓦片审计②）：FetchedAt 超 PoolStaleMax=真——冻结值
	// 仍计 NAV 但显式打标（D-604：stale 显式标记不冒当前）。SumNav 统一重判，
	// fake/直产池集同标。
	Stale bool
	// D-516 共享持有归属：Balance/Equity/FreeMargin 已乘 Share=归属份；
	// Custodial=代管余量（equity_raw×(1−share)——展示标用，不进判据）。
	Share     float64
	Custodial float64
}

// ExplorerIdleFunds ⑤资金不眠异常：fin_venue_account_snapshot 每 venue 最新行 +
// income_events 近30d 合计 + 快照 buffer_multiple，闲置/破地板标记 Go 算。
func (s *Store) ExplorerIdleFunds(ctx context.Context, now time.Time) ([]ExplorerIdlePool, error) {
	// 每键最新行=递归松索引扫描键枚举 + 逐键 LATERAL 下探（1031 覆盖索引前
	// DISTINCT ON 全表序实测 SG 60.8s——834k 行 seq scan+磁盘外排；朴素 LATERAL
	// 余 3.8s 瓶颈在 DISTINCT 键枚举 seq 扫）。loose index scan=递归按
	// (venue,venue_type,currency) 索引序逐键跳跃下探（~115 键×O(log n)）；
	// ev=同键 15min 鲜度窗内最新事件行（D-544 读序：okx_event/binance_event/
	// mt_frame=venue 原生推送真值优先），缺席/陈窗才落 la=全类型最新行
	// （rest_calib/rest_poll/reconnect_resync=愈合补写非权威）。
	// 键列 NOT NULL（实拍零 NULL，'' 残影行由 collapse 段处理）→等值用 `=`。
	// 池纪律：liveEquityMapFn=acctMon.Refresh 全 venue poll（内部再取池连）
	// 必须在任何 pool.Query 之前完成——占连跨 poll=池内自死锁（10-20 生产
	// wedge 实证：conn 挂 idle+Client 15min，200 goroutine 堆 Acquire）。
	var liveMap map[string]float64
	if liveEquityMapFn != nil {
		if m, err := liveEquityMapFn(ctx); err == nil {
			liveMap = m
		}
	}
	rows, err := s.pool.Query(ctx, `
		WITH RECURSIVE keys AS (
			(SELECT venue, venue_type, currency FROM fin_venue_account_snapshot
			 ORDER BY venue, venue_type, currency LIMIT 1)
			UNION ALL
			SELECT n.* FROM keys k
			CROSS JOIN LATERAL (
				SELECT s.venue, s.venue_type, s.currency
				FROM fin_venue_account_snapshot s
				WHERE (s.venue, s.venue_type, s.currency) > (k.venue, k.venue_type, k.currency)
				ORDER BY s.venue, s.venue_type, s.currency
				LIMIT 1
			) n
		)
		SELECT k.venue, k.venue_type,
			COALESCE(ev.balance_raw,      la.balance_raw, 0),
			COALESCE(ev.equity_raw,       la.equity_raw, 0),
			COALESCE(ev.free_margin_raw,  la.free_margin_raw, 0),
			k.currency,
			COALESCE(ev.fetched_at,       la.fetched_at),
			COALESCE(ev.buffer_multiple,  la.buffer_multiple, 0)
		FROM keys k
		CROSS JOIN LATERAL (
			SELECT s.balance_raw, s.equity_raw, s.free_margin_raw, s.fetched_at, s.buffer_multiple
			FROM fin_venue_account_snapshot s
			WHERE s.venue = k.venue AND s.venue_type = k.venue_type AND s.currency = k.currency
			ORDER BY s.fetched_at DESC, s.id DESC
			LIMIT 1
		) la
		LEFT JOIN LATERAL (
			SELECT s.balance_raw, s.equity_raw, s.free_margin_raw, s.fetched_at, s.buffer_multiple
			FROM fin_venue_account_snapshot s
			WHERE s.venue = k.venue AND s.venue_type = k.venue_type AND s.currency = k.currency
				AND s.src IN ('okx_event','binance_event','mt_frame')
				AND s.fetched_at > now() - interval '15 minutes'
			ORDER BY s.fetched_at DESC
			LIMIT 1
		) ev ON true`)
	if err != nil {
		return nil, fmt.Errorf("explorer idle snapshots: %w", err)
	}
	var pools []ExplorerIdlePool
	for rows.Next() {
		var p ExplorerIdlePool
		if err := rows.Scan(&p.Venue, &p.VenueType, &p.Balance, &p.Equity,
			&p.FreeMargin, &p.Currency, &p.FetchedAt, &p.BufferMultiple); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan idle snapshot: %w", err)
		}
		if v, ok := liveMap[p.Venue+":"+p.VenueType+":"+p.Currency]; ok {
			p.Equity = v
		}
		pools = append(pools, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// 显式收连再放行后续查询——defer 会把连一直压到函数返回，下方三次
	// Acquire 期间全持连=雪崩泵源（10-20 wedge 实证的同族形）。
	rows.Close()
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
		WHERE ts >= $1 AND NOT is_transfer GROUP BY venue_key`, now.Add(-30*24*time.Hour))
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
