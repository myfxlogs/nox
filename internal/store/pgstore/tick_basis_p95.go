// tick_basis_p95.go —— D-544：ticks 簿双腿对齐 adverse 成本 p95（非 mt×cex 结构锚）。
// 语义：每个 buy 腿 tick 回配 60s 内最近 sell 腿 tick（ASOF LATERAL，生产干跑实证
// SKLUSDT spot×perp 22 对 p95=39.29bps；KB design-sql-must-be-dry-run——定宽桶 0 对齐被照出）。
// 对齐不对称性如实注：buy 侧每个 tick 回配 sell 侧最近 tick——慢腿主导配对密度，
// 慢速对样本稀即如实小样本（禁前向/双向配对伪造密度）。
package pgstore

import (
	"context"
	"fmt"
	"nox/internal/data/keydomain"
	"time"
)

// TickAdverseBasisP95 ticks 实算「入场即付 adverse 成本」p95（D-544）。
// 序列=(buy.ask − sell.bid)/buy.mid*1e4 bps，since 起 trailing 窗 percentile_cont(0.95)；
// 无对齐样本→(0,false,nil)（与 XAssetMonitorSpreadP95 同约）。
func (s *Store) TickAdverseBasisP95(ctx context.Context, buyVenueKey, buySym, sellVenueKey, sellSym string, since time.Time) (float64, bool, error) {
	var p95 *float64
	// venue_key=生成列零索引——复合键拆 (venue,venue_type) 双参（D-633 返修同族形：
	// ticks_key_ts_idx 三前缀全中 venue+venue_type+symbol+ts 非 ts 位图）。
	bk, sk := keydomain.Parse(buyVenueKey), keydomain.Parse(sellVenueKey)
	err := s.pool.QueryRow(ctx, `
		SELECT percentile_cont(0.95) WITHIN GROUP (ORDER BY (a.ask - s.bid)/a.mid*1e4)
		FROM (SELECT ts, ask, (bid+ask)/2 AS mid
		        FROM ticks WHERE venue=$1 AND venue_type=$2 AND symbol=$3 AND ts>=$4) a
		JOIN LATERAL (
		      SELECT bid FROM ticks
		       WHERE venue=$5 AND venue_type=$6 AND symbol=$7
		         AND ts<=a.ts AND ts>=a.ts - interval '60 seconds'
		       ORDER BY ts DESC LIMIT 1) s ON true`,
		bk.Venue, bk.Type, buySym, since, sk.Venue, sk.Type, sellSym).Scan(&p95)
	if err != nil {
		return 0, false, fmt.Errorf("tick adverse basis p95: %w", err)
	}
	if p95 == nil {
		return 0, false, nil
	}
	return *p95, true, nil
}
