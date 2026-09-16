package pgstore

import (
	"context"
	"fmt"
	"time"
)

// tableHealthQueries 白名单：name → 常量 SQL（无外部输入拼接，无 SQL 注入面）。
// 表名/列名均为编译期常量，TableMaxTs 只按 name 查白名单执行对应 SQL。
// 空表 max() 返回 NULL → pgx 扫入 time.Time 零值（调用方视为 crit）。
var tableHealthQueries = map[string]string{
	"cmc_prices":           "SELECT max(fetched_at) FROM fin_cmc_prices",
	"cmc_account":          "SELECT max(fetched_at) FROM fin_cmc_account_summary",
	"cmc_history":          "SELECT max(fetched_at) FROM fin_cmc_history",
	"xasset_monitor":       "SELECT max(ts) FROM fin_xasset_monitor",
	"cex_spread":           "SELECT max(ts) FROM fin_cex_spread",
	"funding_hist_binance": "SELECT max(ts) FROM facts WHERE kind='funding_history' AND venue='binance'",
	"trx_account":          "SELECT max(fetched_at) FROM fin_trx_account",
	"daily_income":         "SELECT max(day) FROM position_daily_income",
}

// TableMaxTs 返回监控项 name 对应表的最新时间戳（D-301a S1）。
// 白名单未命中 → error（防绕过白名单注入外部表名）。
// 空表 → 零值时间（调用方视为 crit）。只读查询，无写。
func (s *Store) TableMaxTs(ctx context.Context, name string) (time.Time, error) {
	q, ok := tableHealthQueries[name]
	if !ok {
		return time.Time{}, fmt.Errorf("table-health: unknown watch %q", name)
	}
	var ts time.Time
	if err := s.pool.QueryRow(ctx, q).Scan(&ts); err != nil {
		return time.Time{}, fmt.Errorf("table-health %s: %w", name, err)
	}
	return ts, nil
}
