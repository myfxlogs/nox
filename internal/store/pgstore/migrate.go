package pgstore

import (
	"context"
	"fmt"
	"strings"

	"arb/migrations"
)

// Migrate 执行未应用的 migrations（§11.2，core 启动时自跑）。
// 顺序执行 embed 的 SQL 文件，schema_migrations 表记录已应用版本。
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     INTEGER PRIMARY KEY,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	// 检查是否已应用 migration 1
	var applied int
	err = s.pool.QueryRow(ctx, `SELECT 1 FROM schema_migrations WHERE version = 1`).Scan(&applied)
	if err == nil {
		return nil // 已应用，跳过
	}

	sqlText := migrations.InitSQL
	statements := splitStatements(sqlText)
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w\nstatement: %s", err, truncate(stmt, 200))
		}
	}

	_, err = s.pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES (1) ON CONFLICT DO NOTHING`)
	if err != nil {
		return fmt.Errorf("record migration: %w", err)
	}
	return nil
}

// splitStatements 简单按分号分割 SQL 语句（0001_init.sql 无函数体，够用）。
func splitStatements(sql string) []string {
	return strings.Split(sql, ";")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
