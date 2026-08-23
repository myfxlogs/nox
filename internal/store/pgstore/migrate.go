package pgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"arb/migrations"
	"github.com/jackc/pgx/v5"
)

// migrationFiles 按版本号顺序执行；新增版本在此追加。
var migrationFiles = []struct {
	version int
	sql     string
}{
	{1, migrations.InitSQL},
	{2, migrations.BrokerAccountsSQL},
	{3, migrations.BrokerAccountsShortenSQL},
	{4, migrations.BrokerCommissionSQL},
	{5, migrations.BrokerSymbolMapContractSQL},
	{6, migrations.PersonalPositionsSQL},
	{7, migrations.InvestmentRecordsSQL},
	{8, migrations.PersonalLedgerSQL},
	{9, migrations.OpportunitiesCanonicalSQL},
	{10, migrations.ExchangeConfigsSQL},
	{11, migrations.ExchangeSimulatedSQL},
	{12, migrations.CanonicalUsdtSQL},
	{32, migrations.PerpFundingTypeSQL},
	{33, migrations.AutotradeSQL},
	{34, migrations.AutotradeStateSQL},
	{35, migrations.StatsAnchorSQL},
	{36, migrations.ComboIdentitySQL},
	{37, migrations.VerificationStatsSQL},
	{38, migrations.SettlementTypedColumnsSQL},
	{39, migrations.WatchlistSQL},
}

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

	for _, m := range migrationFiles {
		applied, err := s.isApplied(ctx, m.version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		for _, stmt := range splitStatements(m.sql) {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := s.pool.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("migrate v%d: %w\nstatement: %s", m.version, err, truncate(stmt, 200))
			}
		}
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, m.version,
		); err != nil {
			return fmt.Errorf("record migration v%d: %w", m.version, err)
		}
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

// splitStatements 按分号分割 SQL 语句，但跳过 $$ ... $$ 引号块内的分号
// （D-072 migration 0034 使用 DO $$ ... $$; 块，简单 Split 会破坏函数体）。
func splitStatements(sql string) []string {
	var stmts []string
	var buf strings.Builder
	inDollar := false
	for i := 0; i < len(sql); i++ {
		if i+1 < len(sql) && sql[i] == '$' && sql[i+1] == '$' {
			inDollar = !inDollar
			buf.WriteByte('$')
			buf.WriteByte('$')
			i++
			continue
		}
		if sql[i] == ';' && !inDollar {
			stmts = append(stmts, buf.String())
			buf.Reset()
			continue
		}
		buf.WriteByte(sql[i])
	}
	if buf.Len() > 0 {
		stmts = append(stmts, buf.String())
	}
	return stmts
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
