package pgstore

import (
	"context"
	"errors"
	"fmt"

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
	{103, migrations.IncomeEventsSQL},                  // D-334ab 位点①：income_events 实数轨（tranId 三列唯一幂等）
	{104, migrations.XassetNoiseSQL},                   // D-335 S1：fin_xasset_monitor noise_bps 真噪声列（24h 摆幅半径）
	{105, migrations.XassetAllowCombosSQL},             // D-338 S1：XASSET 白名单表化（播种四行=.env:27 逐字）
	{106, migrations.HarvestCycleSQL},                  // D-341 S1：收割线/学习样本锚/exit_kind/v_cycle_realrate 视图
	{107, migrations.CarryUniverseSQL},                 // D-336 S2：carry 宇宙扫描快照表（发现层）
	{108, migrations.CarryUniverseNoteSQL},             // D-345：suspect 门注记列
	{109, migrations.PaperSpotCarrySQL},                // D-350：spot-carry 纸面验证轨
	{110, migrations.PaperBasisSQL},                    // D-354：纸面仓盈亏视图（基差采集+两列）
	{111, migrations.VenueAccountSnapshotSQL},          // D-358 S1：CEX/MT 账户余额快照（原币 _raw+currency）
	{112, migrations.VenueSnapshotRealOnlySQL},         // D-360 S3：快照只留 real（MT demo NOT EXISTS/CEX sim EXISTS 清理）
	{113, migrations.OkxUnifiedCleanupSQL},             // D-361 S3：OKX 统一账户旧 spot/perp 双行清场（写侧归一 unified）
	{114, migrations.CexSubaccountSeedsSQL},            // D-363 S1：funding/earn 子账户 config 种子（凭据 SELECT 复用/幂等）
	{115, migrations.FinVenueIOSQL},                    // D-365 S1：CEX 进出账流水（幂等四元唯一键+ts 倒序索引）
	{116, migrations.XassetComboNotionalSQL},           // D-367 S1：组合级 sizing 名义额列（NULL=回退全局 env）
	{117, migrations.AppConfigSQL},                     // D-368 S1：运维旋钮 KV（Bucket B 首批，零种子）
	{118, migrations.RadarWatchlistSQL},                // D-369 S2：确认信号 watchlist（radar 写/core 读）
	{119, migrations.PaperCfdColumnsSQL},               // D-374 S2：纸面轨扩 cfd×perp（structure/mt_venue 两列）
	{120, migrations.PositionEvalsSQL},                 // D-377 S1：XASSET 退出评估观测轨（position_evals 窄行表）
	{121, migrations.VenueAccountSnapshotSrcSQL},       // D-383 B1/B2：快照行写路标记 src 列（事件 mt_event/okx_event，默认 scheduler）
	{122, migrations.BrokerSymbolMapIsPrimarySQL},      // D-383 A2.1/A2.6：is_primary+部分唯一索引+单行组固化+27 组种子
	{123, migrations.RadarDigestStateSQL},              // D-389 S1：digest 边沿化集合 hash 状态表
	{124, migrations.RadarDigestMemberSQL},             // D-392 S1：digest 成员级边沿（新入才推+flap TTL）
	{125, migrations.CarryGraduationsSQL},              // D-403 S1：验证毕业链建议表
	{126, migrations.MarginGuardSQL},                   // D-405 S1：snapshot buffer_multiple + fin_venue_margin_status
	{127, migrations.AlertsPositionIDSQL},              // D-411 S1：alerts.position_id 持仓级告警列
	{128, migrations.PaperAllInSQL},                    // D-410 S1：纸面全账三列+存量费用回填
	{129, migrations.XassetMonitorVenueSQL},            // D-423：fin_xasset_monitor cex/mt venue 三元组（存量=binance:perp×3001157:mt5）
	{130, migrations.CashflowsLegDayEventKeySQL},       // D-422 R1：腿日唯一索引含 event_key（OPEN费+CLOSE佣金分键）
	{131, migrations.StrategyReasonD424SQL},            // D-424：收敛型退出腿序修正归因作废注记
	{132, migrations.DataPlaneVenueSQL},                // D-420 S2/S3：fin_cex_depth+油族 CL 映射对齐
	{133, migrations.PaperIntraTradesSQL},              // D-425 S1：日内单品高风险轨纸面表
	{134, migrations.AutotradeAllowRulesSQL},           // D-443 S1：autotrade venue/canonical 白名单表化+env seed
	{135, migrations.IntraEventsSQL},                   // D-441 S2：日内轨栅栏事件表（0134撞D-443改135）
	{136, migrations.CexPrivateEventsSQL},              // D-450 S4：CEX 私有 WS 事件审计表
	{137, migrations.SymdigSQL},                        // D-464 S1/S2：fin_symbol_aliases + fin_symdig_findings
	{138, migrations.VenueSymbolFlagsSQL},              // D-463 S1/S2：fin_venue_symbol_flags + carry_graduations.flag_warn
	{139, migrations.PaperMissStreakSQL},               // D-474a S4：paper_spot_carry.miss_streak 缺席独立计数
	{140, migrations.VenueExposureCapsSQL},             // D-475a S1+S5：敞口闸表+paper 质量杆观测两列
	{141, migrations.PerpFundingAdmissionSQL},          // D-491 S1：PERP_FUNDING 证据准入表（白名单退役）
	{142, migrations.KeydomainConstitutionSQL},         // D-496：键域宪法全库归一（venue/venue_type/venue_key generated）
	{143, migrations.CarryGraduationPairColsSQL},       // D-498：carry_graduations venue_a/venue_b 源列+venue 展示投影
	{144, migrations.D499TailColsSQL},                  // D-499：carry venue_type 拆除+flags 配对域拆列
	{145, migrations.ExecControlBrokerScopeSQL},        // D-493：execution_control_events scope_type 补 'broker' 审计域
	{146, migrations.DropBinanceDemoConfigSQL},         // D-504：删 binance demo 配置行
	{147, migrations.FactsKindIndexPgstatSQL},          // D-508 S3：facts (kind,venue,symbol,ts desc) 索引+pg_stat_statements
	{148, migrations.PositionsLegacyDemoSQL},           // D-512 T8：positions.environment NULL→legacy_demo（纯 UPDATE）
	{149, migrations.SettlementIncidentAttributedSQL},  // D-512 T9：settlements 事故归因列+存量按同判据回填
	{150, migrations.ExplorerShellSQL},                 // D-513 S2：explorer_proposals/explorer_state 记分板骨架双表
	{151, migrations.ConfigToPgSQL},                    // D-515：app_secrets+swaparb_allow_combos+exchange enc 三列
	{152, migrations.MacroTablesSQL},                   // D-528：macro_event+cot_position+fred_observation 三表
	{153, migrations.RadarWatchlistCanonicalSQL},       // D-532 S2：watchlist symbol 方言归一（venues[1] 后缀反推）
	{154, migrations.CalibTablesSQL},                   // D-533：calib_scoreboard+calib_params 两表+种值四行
	{155, migrations.IdleFloorStreakSQL},               // D-534 B4：睡钱触发层连续天数状态表
	{156, migrations.EndpointFossilSeedSQL},            // 化石批 691544b2 落了文件漏注册——D-543 补登（WHERE 守卫幂等，生产零行为变化）
	{157, migrations.ExposureCapsSeedSQL},              // D-543：fin_venue_exposure_caps 种子五行（幂等）
	{159, migrations.ExposureCapTruthFixSQL},           // D-547：组合帽对齐 2×notional+okx:unified→okx:perp 重键名
	{160, migrations.PaperDualCexColumnsSQL},           // D-555 S1：paper/carry_graduations venue_type_a/b+venue_key_a/b 生成列
	{161, migrations.QuoteUnitScalesSQL},               // D-594：报价单位归一层（price_scale/qty_scale+铜定标+XCU 别名）
	{162, migrations.PositionFeesNetPnlSQL},            // D-599：positions fees_usd/fee_quality（realized_pnl 净额化）
	{163, migrations.StrategyStatsNotifySQL},           // D-004 P1#3：disabled 变更 pg_notify（事件源替代 60s 轮询）
	{164, migrations.CanonicalExposureCapsSQL},         // D-006 S3：fin_canonical_exposure_caps 聚簇闸表
	{165, migrations.MT5ExposureCap6000SQL},            // D-007 S2：MT5 两户 venue 敞口闸 $6000（业主批）
	{166, migrations.MarginSemanticsSQL},               // D-010：margin_usd 列+互斥；venue cap 语义翻保证金
	{167, migrations.LegExternalStatusSQL},             // D-015 ②：position_legs.status 增 EXTERNAL
	{168, migrations.D013LedgerTruthSQL},               // D-013：partial 合法化 + #497–507 账本回填
	{169, migrations.LegFillKindSQL},                   // D-018 阶段a：position_legs.fill_kind（maker|taker；168 已被 D-013 占用）
	{170, migrations.BrokerSwapFreeSQL},                // D-014 S1：broker_accounts.swap_free 旗标列 + taker_fee_bps（R1：169 撞号改 170）
	{171, migrations.MakerFeeEnableSQL},                // D-018 后段：exchange_configs.maker_fee_bps + app_config maker 启用键
	{172, migrations.IncidentExitKindCleanupSQL},       // D-032 R1：清零成交 FAILED 误打的 legged_unwind
	{173, migrations.PaperWindowStartSQL},              // D-033①：app_config 播种 ARB_PAPER_WINDOW_START
	{174, migrations.DropOpportunitiesTickSQL},         // D-040②：opportunities_tick 死表 DROP（零引用，已备份）
	{175, migrations.ProbePositionsSQL},                // D-047①：positions +decision_ref/auto_close_at（PROBE 单腿探针）
	{176, migrations.ProbeLLMLoopSQL},                  // D-048①②：explorer_proposals 探针提案契约+审批流转列
	{177, migrations.MacroZhResetSQL},                  // D-053①：mql5 源 en 旧行清除（zh 端点切换）
	{178, migrations.DropPerpFundingAdmissionSQL},      // D-057⑤：PERP_FUNDING 证据准入表退役（已备份）
	{179, migrations.RetireUniverseAlertsSQL},          // D-072：universe 发现告警退役——清历史 info 噪音行
	{180, migrations.D120RetireResearchSQL},            // D-120：研究面 A 批 13 表幂等 DROP（已备份）
	{181, migrations.FactsNullSymbolIdxSQL},            // D-121：facts NULL-symbol 组枚举部分索引（0180 撞号顺延）
	{182, migrations.BinanceEarnSeedSQL},               // D-130 R1：binance:earn 凭据复制补种 + binance:funding 行摘除（A 案）
	{183, migrations.DropBinanceEarnSQL},               // D-133：binance earn 面整体摘除（counter-migration 收口 0182）
	{184, migrations.DropCnfiRepoSQL},                  // D-139：CNFI/逆回购面 13 表幂等 DROP（已备份）
	{185, migrations.FactsKindTsIdxSQL},                // D-152 fixup：facts(kind,ts) retention 候选寻址
	{186, migrations.ElimEvidenceSeedSQL},              // D-180：统计证据阈值 app_config 播种
	{187, migrations.CexDCIQuoteSQL},                   // D-183：三所 DCI/双币产品报价表
	{189, migrations.CapabilityConstraintSQL},          // D-186②：能力约束表 fin_capability_constraint
	{190, migrations.ConditionGapSQL},                  // D-186③：条件缺口表 fin_condition_gap
	{191, migrations.CapabilityPermissionSQL},          // D-188②：权限探测面 fin_capability_permission
	{192, migrations.DCIOrderSQL},                      // D-191：DCI 申购幂等账本 fin_dci_order
	{193, migrations.EmployeeRegistrySQL},              // D-190：员工花名册 fin_employee_registry
	{194, migrations.ProposalSQL},                      // D-189：提案实体化 fin_proposal+dci_order 回链
	{195, migrations.GapResolveNoteSQL},                // D-193②：缺口收口备注列
	{196, migrations.OTMBoundSQL},                      // D-191-R9：%OTM 闸界入约束表
	{197, migrations.DCIOrderRefSQL},                   // D-191-R10：报价行 order_ref 补列
	{198, migrations.DCIAmountBoundSQL},                // D-191-R11：报价行 min/max/decimal 三字段
	{199, migrations.DormantDeferredSQL},               // D-204：deferred 态+休眠轨种子
	{188, migrations.OppTemplateSQL},                   // D-185：机会模板册（fin_opp_template+cond）
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

	// D-508 S6：自研分割器拆除——pgx simple protocol 原生支持多语句批
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
