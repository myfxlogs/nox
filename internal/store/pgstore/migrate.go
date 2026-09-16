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
	{77, migrations.FeishuPushTargetSQL},               // D-264 飞书告警推送目标
	{78, migrations.RepoRatesSQL},                      // D-266 CNY 逆回购利率雷达
	{79, migrations.TrxVoteRebalanceSQL},               // D-269 TRX 投票收益优化器
	{80, migrations.CnfiRepoOpsSQL},                    // D-270 逆回购周四操作闭环
	{81, migrations.TrxAccountTronPowerSQL},            // D-211 返修：补 tron_power 列（质押量）
	{82, migrations.CnfiRepoConfigSQL},                 // D-270 R2 逆回购提醒配置 DB 化
	{83, migrations.CexScanSQL},                        // D-271 CEX 只读扫描器
	{84, migrations.ExchangeConfigsUniqueSimulatedSQL}, // D-271 R1：exchange_configs 唯一约束加 simulated
	{85, migrations.RepoTradesSQL},                     // D-272 逆回购操作记录+策略验证闭环
	{86, migrations.CexEarnJudgmentSQL},                // D-273：earn APR 判断引擎
	{87, migrations.RepoTradesFeeSQL},                  // D-276 逆回购手续费模型
	{88, migrations.TrxAccountResourceSQL},             // D-278 S5 地址资源显示列
	{89, migrations.TrxTxsCheatStatusSQL},              // D-279 TRX 交易 cheat_status 列
	{90, migrations.TrxTxsKindWidenSQL},                // D-279 kind 列扩容 varchar(32)
	{91, migrations.PriceAlertsSQL},                    // D-282 价格提醒规则引擎
	{92, migrations.TrxAccountRewardNumSQL},            // D-285 TRX 待领取奖励真值列
	{93, migrations.SystemSettingsSQL},                 // D-296 系统配置单行表（tronscan_base）
	{94, migrations.TrxVoteAprMinRateSQL},              // D-297 TRX 投票 APR 加 min_rate/min_sr 列
	{95, migrations.XassetMonitorSQL},                  // D-299 PAXG-XAU 跨市场套利监控快照
	{96, migrations.EventCasesPaperTradesSQL},          // D-274 事件驱动研判引擎：event_cases + paper_trades
	{97, migrations.XassetCarrySQL},                    // D-300 跨标的同质资产套利：asset_equiv + opportunities XASSET_CARRY（种子 GOLD=PAXG+XAUT+XAU 三行，裁 12 并入）
	{98, migrations.XauusdtGoldSQL},                    // D-303 GOLD+XAUUSDT
	{99, migrations.CexUniverseSQL},                    // D-307 CEX 全量合约雷达：universe/events/radar 三表
	{100, migrations.T1PromotionSQL},                   // D-308 非加密 T1 晋级：asset_equiv 十类+XAGUSD 腿映射
	{101, migrations.CanonicalFixSQL},                  // D-308 勘误：perp/mt5 canonical 回正到 ticks 命名空间
	{102, migrations.XassetExecSQL},                    // D-311 XASSET_CARRY 通路：positions CHECK 七值 + universe ct_val
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
