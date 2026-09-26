// ticks.go —— D-495 S1：腿可平性谓词的数据面（ticks 表窗口内 bid 值变化计数）。
// 判据出处：KB heartbeat-quote-fools-freshness-gate——可交易=窗口内 bid 值发生变化≥2 种；
// 单值（冻结报价/冷启动首见）与时间戳新鲜均不等于可交易。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// TicksPriceChanged 窗口内 (venue, symbol) 的 DISTINCT bid 数。
// changed = n >= 2（价格动过=市场活在撮合）；n 供调用方拼诊断文案。
// venue/symbol 是 ticks 表原生键（裸 venue 名 + venue 原生 symbol 或 CEX 归一键），
// 键域映射由调用方负责（leg_actionable.go），本函数不做键形转换。
func (s *Store) TicksPriceChanged(ctx context.Context, venue, symbol string, window time.Duration) (changed bool, n int, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT bid)::int FROM ticks
		WHERE venue = $1 AND symbol = $2 AND ts > now() - $3::interval`,
		venue, symbol, window.String()).Scan(&n)
	if err != nil {
		return false, 0, fmt.Errorf("ticks price changed %s/%s: %w", venue, symbol, err)
	}
	return n >= 2, n, nil
}
