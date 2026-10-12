// db.go PG 只读：读 autotrade_logs（REJECTED/FAILED）+ alerts（critical）增量（S2）。
package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// autotradeLogRow autotrade_logs 增量行（digest 所需字段）。
type autotradeLogRow struct {
	ID        int64
	Reason    string
	Canonical string
}

// alertRow alerts 增量行（digest 所需字段）。
type alertRow struct {
	ID      int64
	Message string
}

// readAutotradeLogs 读 autotrade_logs: id > 水位 AND status IN ('REJECTED','FAILED')，按 id 升序。
func readAutotradeLogs(ctx context.Context, pool *pgxpool.Pool, waterID int64) ([]autotradeLogRow, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, COALESCE(reason,''), COALESCE(canonical,'')
		 FROM autotrade_logs
		 WHERE id > $1 AND status IN ('REJECTED','FAILED')
		 ORDER BY id ASC`, waterID)
	if err != nil {
		return nil, fmt.Errorf("query autotrade_logs: %w", err)
	}
	defer rows.Close()
	var out []autotradeLogRow
	for rows.Next() {
		var r autotradeLogRow
		if err := rows.Scan(&r.ID, &r.Reason, &r.Canonical); err != nil {
			return nil, fmt.Errorf("scan autotrade_logs: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// readAlerts 读 alerts: id > 水位 AND level='critical'，按 id 升序。
func readAlerts(ctx context.Context, pool *pgxpool.Pool, waterID int64) ([]alertRow, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, message FROM alerts WHERE id > $1 AND level='critical' ORDER BY id ASC`,
		waterID)
	if err != nil {
		return nil, fmt.Errorf("query alerts: %w", err)
	}
	defer rows.Close()
	var out []alertRow
	for rows.Next() {
		var r alertRow
		if err := rows.Scan(&r.ID, &r.Message); err != nil {
			return nil, fmt.Errorf("scan alerts: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
