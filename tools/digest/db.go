// db.go PG 只读：读 autotrade_logs（REJECTED/FAILED）+ alerts（critical）增量（S2）。
package main

import (
	"context"
	"fmt"
	"log"

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

// clampWatermark 化石水位自愈：存量水位 > 库内实存 max(id) → 钳回实存上限。
// 库迁移/重建后旧水位令增量面永久空读（SG 实证 alerts 水位 64904 > max 289=迁移遗留）；
// 钳位仅向前回退水位，不丢未读增量（水位>max 时 id≤max 的行本就不可能未读）。
func clampWatermark(ctx context.Context, pool *pgxpool.Pool, s *digestState) error {
	var logMax, alertMax int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(max(id),0) FROM autotrade_logs`).Scan(&logMax); err != nil {
		return fmt.Errorf("max autotrade_logs: %w", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(max(id),0) FROM alerts`).Scan(&alertMax); err != nil {
		return fmt.Errorf("max alerts: %w", err)
	}
	if s.AutotradeLogsMaxID > logMax {
		log.Printf("digest: 化石水位 autotrade_logs %d→%d（库内实存上限）", s.AutotradeLogsMaxID, logMax)
		s.AutotradeLogsMaxID = logMax
	}
	if s.AlertsMaxID > alertMax {
		log.Printf("digest: 化石水位 alerts %d→%d（库内实存上限）", s.AlertsMaxID, alertMax)
		s.AlertsMaxID = alertMax
	}
	return nil
}
