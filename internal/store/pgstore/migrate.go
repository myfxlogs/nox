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
	{40, migrations.WatchlistVenuesSQL},
	{41, migrations.AnalystRoRoleSQL},
	{42, migrations.AiSettingsSQL},
	{43, migrations.PositionSwapSnapshotsSQL},
	{44, migrations.BrokerSymbolMapSourceSQL},
	{45, migrations.BrokerAccountsIsRealSQL},
	{46, migrations.AccountWebsiteSQL},
	{47, migrations.ArbRoRevokeCredentialsSQL},
	{48, migrations.PositionIntentFieldsSQL},
	{49, migrations.CashflowsLegNoSQL},
	{50, migrations.BrokerSymbolMapProductGroupSQL},
	{51, migrations.DeletePreciousMetalsCrossCurrencySQL},
	{52, migrations.PositionNotionalUsdSQL},
	{53, migrations.PositionClosingStartedAtSQL},
	{54, migrations.CashflowsD193SQL},
	{55, migrations.SettlementsD193SQL},
	{56, migrations.CapitalAccountsSQL},
	{57, migrations.CapitalSnapshotsSQL},
	{58, migrations.LedgerD193SQL},
	{59, migrations.HypothesesSQL},
	{60, migrations.CapitalDecisionsSQL},
	{61, migrations.PositionReviewsSQL},
	{62, migrations.ExecutionControlsSQL},
	{63, migrations.PositionLegsAttemptSQL},
	{64, migrations.LiveGateSQL},
	{65, migrations.CmcTablesSQL},
	{66, migrations.PersonalCMCLinkSQL},
	{67, migrations.CMCPositionsMetaSQL},
	{68, migrations.TrxStakeTablesSQL},
	{69, migrations.CmcWsEventDrivenSQL},
	{70, migrations.CnfiTablesSQL},
	{71, migrations.TrxVoteAprSQL},
	{72, migrations.TrxCycleViSQL},
	{73, migrations.CnfiHoldingsSQL},
	{74, migrations.CnfiActionsSQL},
	{75, migrations.CnfiCandidatePoolSQL},
	{76, migrations.TrxVoteAprTopSQL},
	{77, migrations.FeishuPushTargetSQL}, // D-264 飞书告警推送目标
	{78, migrations.TrxVoteRebalanceSQL}, // D-269 TRX 投票收益优化器
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
		// D-249 S5 审计 P1-6：每个 migration file 在单连接事务内执行（BEGIN/COMMIT），
		// 防部分失败后 DB 处于"部分应用+未记录"态（0036 RENAME 链最脆弱）。
		// 事务内含 schema_migrations 记录写入，确保"应用成功+版本记录"原子化。
		if err := s.applyMigrationTx(ctx, m.version, m.sql); err != nil {
			return err
		}
	}
	return nil
}

// applyMigrationTx 在单连接事务内执行一个 migration file 的全部语句 + 记录版本号。
// D-249 S5 审计 P1-6：per-migration 事务包裹——任一语句失败 → ROLLBACK，DB 不留半途残留，
// schema_migrations 不记录该版本 → 下次启动可重入重跑（先红：半途残留；后绿：重跑无残留）。
// 注意：migration SQL 内禁用 BEGIN/COMMIT（0055 已更正）——事务由本方法统一管理，
// SQL 内嵌 BEGIN 在 pgx 事务内会产生 WARNING 且被忽略，COMMIT 会提前提交外层事务。
func (s *Store) applyMigrationTx(ctx context.Context, version int, sql string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migrate v%d begin: %w", version, err)
	}
	defer tx.Rollback(ctx) // commit 后 no-op

	for _, stmt := range splitStatements(sql) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("migrate v%d: %w\nstatement: %s", version, err, truncate(stmt, 200))
		}
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
