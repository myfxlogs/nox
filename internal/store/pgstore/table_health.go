package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// tableHealthQueries 白名单：name → 常量 SQL（无外部输入拼接，无 SQL 注入面）。
// 表名/列名均为编译期常量，TableMaxTs 只按 name 查白名单执行对应 SQL。
// 空表 max() 返回 NULL → pgtype.Timestamp.Valid=false → 返回零值 time（调用方视为 crit）。
var tableHealthQueries = map[string]string{
	"cmc_prices":           "SELECT max(fetched_at) FROM fin_cmc_prices",
	"cmc_account":          "SELECT max(fetched_at) FROM fin_cmc_account_summary",
	"cmc_history":          "SELECT max(fetched_at) FROM fin_cmc_history",
	"xasset_monitor":       "SELECT max(ts) FROM fin_xasset_monitor",
	"cex_spread":           "SELECT max(ts) FROM fin_cex_spread",
	"funding_hist_binance": "SELECT max(ts) FROM facts WHERE kind='funding_history' AND venue='binance'",
	"trx_account":          "SELECT max(fetched_at) FROM fin_trx_account",
	// day 是 DATE（日粒度），新鲜基准=覆盖期末 day+1d 的 00:00（健康 age<24h，
	// 漏写一日 >24h 越线）——旧码取行标签起点 max(day)，健康日 age 落 24-48h 假 critical。
	"daily_income": "SELECT max(day)::timestamp + interval '1 day' FROM position_daily_income",
}

// TableMaxTs 返回监控项 name 对应表的最新时间戳（D-301a S1）。
// 白名单未命中 → error（防绕过白名单注入外部表名）。
// 空表 → 零值时间（调用方视为 crit）。只读查询，无写。
// 退回修复：空表 max() 返回 NULL，pgx 不能 Scan NULL→time.Time 零值，
// 改用 pgtype.Timestamp 接收，Valid=false 时返回零值 time + nil error。
func (s *Store) TableMaxTs(ctx context.Context, name string) (time.Time, error) {
	q, ok := tableHealthQueries[name]
	if !ok {
		return time.Time{}, fmt.Errorf("table-health: unknown watch %q", name)
	}
	var ts pgtype.Timestamp
	if err := s.pool.QueryRow(ctx, q).Scan(&ts); err != nil {
		return time.Time{}, fmt.Errorf("table-health %s: %w", name, err)
	}
	if !ts.Valid {
		return time.Time{}, nil // 空表→零值（调用方视为 crit）
	}
	return ts.Time, nil
}
