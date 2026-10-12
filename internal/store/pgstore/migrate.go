package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Migrate 执行未应用的 migrations（§11.2，core 启动时自跑）。
// 顺序执行 embed 的 SQL 文件，schema_migrations 表记录已应用版本。
// 瞬断重试（D-244）：连接层瞬断（server conn kill/restart 窗口，CI EBADF 实证）
// 3 次指退重试——per-migration 事务保证重入安全；非瞬断真错首败即报。
func (s *Store) Migrate(ctx context.Context) error {
	var lastErr error
	for i := 0; i < 3; i++ {
		if lastErr = s.migrateOnce(ctx); lastErr == nil {
			return nil
		}
		if !isTransientConnErr(lastErr) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(i+1) * 300 * time.Millisecond):
		}
	}
	return lastErr
}

func (s *Store) migrateOnce(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     INTEGER PRIMARY KEY,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	// D-214 squash 取代：schema_migrations 已含 ≥1000 baseline marker → <1000 全视为已应用
	//（M3 切流 TRUNCATE+写 1000 后历史链不被误判 pending 重放）。
	var baselineMarked bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version >= 1000)`).Scan(&baselineMarked); err != nil {
		return fmt.Errorf("check baseline marker: %w", err)
	}

	for _, m := range migrationFiles {
		applied, err := s.isApplied(ctx, m.version)
		if err != nil {
			return err
		}
		if applied || (m.version < 1000 && baselineMarked) {
			continue
		}
		if m.baseline {
			// baseline 永不自动执行 SQL——鲜装重放走 1..201 历史链，此处只补 marker。
			if _, err := s.pool.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, m.version); err != nil {
				return fmt.Errorf("mark baseline v%d: %w", m.version, err)
			}
			baselineMarked = true
			continue
		}
		// 审计 P1-6：每个 migration file 在单连接事务内执行（BEGIN/COMMIT），
		// 防部分失败后 DB 处于"部分应用+未记录"态（0036 RENAME 链最脆弱）。
		// 事务内含 schema_migrations 记录写入，确保"应用成功+版本记录"原子化。
		if err := s.applyMigrationTx(ctx, m.version, m.sql); err != nil {
			return err
		}
	}
	return nil
}

// applyMigrationTx 在单连接事务内执行一个 migration file 的全部语句 + 记录版本号。
// 审计 P1-6：per-migration 事务包裹——任一语句失败 → ROLLBACK，DB 不留半途残留，
// schema_migrations 不记录该版本 → 下次启动可重入重跑（先红：半途残留；后绿：重跑无残留）。
// 注意：migration SQL 内禁用 BEGIN/COMMIT（0055 已更正）——事务由本方法统一管理，
// SQL 内嵌 BEGIN 在 pgx 事务内会产生 WARNING 且被忽略，COMMIT 会提前提交外层事务。
func (s *Store) applyMigrationTx(ctx context.Context, version int, sql string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migrate v%d begin: %w", version, err)
	}
	defer tx.Rollback(ctx) // commit 后 no-op

	// 自研分割器拆除——pgx simple protocol 原生支持多语句批
	// （tx 内仍原子：整批在同一事务执行，半途失败整体回滚）。
	if _, err := tx.Exec(ctx, sql, pgx.QueryExecModeSimpleProtocol); err != nil {
		return fmt.Errorf("migrate v%d: %w\nsql: %s", version, err, truncate(sql, 200))
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, version,
	); err != nil {
		return fmt.Errorf("record migration v%d: %w", version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migrate v%d commit: %w", version, err)
	}
	return nil
}

// isApplied 检查版本是否已应用。
func (s *Store) isApplied(ctx context.Context, version int) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx, `SELECT 1 FROM schema_migrations WHERE version = $1`, version).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("check migration v%d: %w", version, err)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
