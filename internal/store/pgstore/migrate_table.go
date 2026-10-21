package pgstore

import "nox/migrations"

// migrationFiles 按版本号顺序执行；新增版本在此追加。
// baseline=true：压平基线（D-214 号段 1000+）——永不自动执行 SQL（鲜装重放=1..201 历史链，
// 生产 M3 切流由 TRUNCATE schema_migrations+写入 1000 收口）；未记录时仅补 marker。
// schema_migrations 一旦出现 ≥1000 版本行，全部 <1000 版本视为已应用（squash 取代语义，
// 防切流 TRUNCATE 后历史链被误判 pending 重放）。
var migrationFiles = []struct {
	version  int
	sql      string
	baseline bool
}{
	{1, migrations.InitSQL, false},
	{2, migrations.BrokerAccountsSQL, false},
	{3, migrations.BrokerAccountsShortenSQL, false},
	{4, migrations.BrokerCommissionSQL, false},
	{5, migrations.BrokerSymbolMapContractSQL, false},
	{6, migrations.PersonalPositionsSQL, false},
	{7, migrations.InvestmentRecordsSQL, false},
	{8, migrations.PersonalLedgerSQL, false},
	{9, migrations.OpportunitiesCanonicalSQL, false},
	{10, migrations.ExchangeConfigsSQL, false},
	{11, migrations.ExchangeSimulatedSQL, false},
	{12, migrations.CanonicalUsdtSQL, false},
	{32, migrations.PerpFundingTypeSQL, false},
	{33, migrations.AutotradeSQL, false},
	{34, migrations.AutotradeStateSQL, false},
	{35, migrations.StatsAnchorSQL, false},
	{36, migrations.ComboIdentitySQL, false},
	{37, migrations.VerificationStatsSQL, false},
	{38, migrations.SettlementTypedColumnsSQL, false},
	{39, migrations.WatchlistSQL, false},
	{40, migrations.WatchlistVenuesSQL, false},
	{41, migrations.AnalystRoRoleSQL, false},
	{42, migrations.AiSettingsSQL, false},
	{43, migrations.PositionSwapSnapshotsSQL, false},
	{44, migrations.BrokerSymbolMapSourceSQL, false},
	{45, migrations.BrokerAccountsIsRealSQL, false},
	{46, migrations.AccountWebsiteSQL, false},
	{47, migrations.ArbRoRevokeCredentialsSQL, false},
	{48, migrations.PositionIntentFieldsSQL, false},
	{49, migrations.CashflowsLegNoSQL, false},
	{50, migrations.BrokerSymbolMapProductGroupSQL, false},
	{51, migrations.DeletePreciousMetalsCrossCurrencySQL, false},
	{52, migrations.PositionNotionalUsdSQL, false},
	{53, migrations.PositionClosingStartedAtSQL, false},
	{54, migrations.CashflowsD193SQL, false},
	{55, migrations.SettlementsD193SQL, false},
	{56, migrations.CapitalAccountsSQL, false},
	{57, migrations.CapitalSnapshotsSQL, false},
	{58, migrations.LedgerD193SQL, false},
	{59, migrations.HypothesesSQL, false},
	{60, migrations.CapitalDecisionsSQL, false},
	{61, migrations.PositionReviewsSQL, false},
	{62, migrations.ExecutionControlsSQL, false},
	{63, migrations.PositionLegsAttemptSQL, false},
	{64, migrations.LiveGateSQL, false},
	{65, migrations.CmcTablesSQL, false},
	{66, migrations.PersonalCMCLinkSQL, false},
	{67, migrations.CMCPositionsMetaSQL, false},
	{68, migrations.TrxStakeTablesSQL, false},
	{69, migrations.CmcWsEventDrivenSQL, false},
	{70, migrations.CnfiTablesSQL, false},
	{71, migrations.TrxVoteAprSQL, false},
	{72, migrations.TrxCycleViSQL, false},
	{73, migrations.CnfiHoldingsSQL, false},
	{74, migrations.CnfiActionsSQL, false},
	{75, migrations.CnfiCandidatePoolSQL, false},
	{76, migrations.TrxVoteAprTopSQL, false},
	{77, migrations.FeishuPushTargetSQL, false},               // D-264 飞书告警推送目标
	{78, migrations.RepoRatesSQL, false},                      // D-266 CNY 逆回购利率雷达
	{79, migrations.TrxVoteRebalanceSQL, false},               // D-269 TRX 投票收益优化器
	{80, migrations.CnfiRepoOpsSQL, false},                    // D-270 逆回购周四操作闭环
	{81, migrations.TrxAccountTronPowerSQL, false},            // D-211 返修：补 tron_power 列（质押量）
	{82, migrations.CnfiRepoConfigSQL, false},                 // D-270 R2 逆回购提醒配置 DB 化
	{83, migrations.CexScanSQL, false},                        // D-271 CEX 只读扫描器
	{84, migrations.ExchangeConfigsUniqueSimulatedSQL, false}, // D-271 R1：exchange_configs 唯一约束加 simulated
	{85, migrations.RepoTradesSQL, false},                     // D-272 逆回购操作记录+策略验证闭环
	{86, migrations.CexEarnJudgmentSQL, false},                // D-273：earn APR 判断引擎
	{87, migrations.RepoTradesFeeSQL, false},                  // D-276 逆回购手续费模型
	{88, migrations.TrxAccountResourceSQL, false},             // D-278 S5 地址资源显示列
	{89, migrations.TrxTxsCheatStatusSQL, false},              // D-279 TRX 交易 cheat_status 列
	{90, migrations.TrxTxsKindWidenSQL, false},                // D-279 kind 列扩容 varchar(32)
	{91, migrations.PriceAlertsSQL, false},                    // D-282 价格提醒规则引擎
	{92, migrations.TrxAccountRewardNumSQL, false},            // D-285 TRX 待领取奖励真值列
	{93, migrations.SystemSettingsSQL, false},                 // D-296 系统配置单行表（tronscan_base）
	{94, migrations.TrxVoteAprMinRateSQL, false},              // D-297 TRX 投票 APR 加 min_rate/min_sr 列
	{95, migrations.XassetMonitorSQL, false},                  // D-299 PAXG-XAU 跨市场套利监控快照
	{96, migrations.EventCasesPaperTradesSQL, false},          // D-274 事件驱动研判引擎：event_cases + paper_trades
	{97, migrations.XassetCarrySQL, false},                    // D-300 跨标的同质资产套利：asset_equiv + opportunities XASSET_CARRY（种子 GOLD=PAXG+XAUT+XAU 三行，裁 12 并入）
	{98, migrations.XauusdtGoldSQL, false},                    // D-303 GOLD+XAUUSDT
	{99, migrations.CexUniverseSQL, false},                    // D-307 CEX 全量合约雷达：universe/events/radar 三表
	{100, migrations.T1PromotionSQL, false},                   // D-308 非加密 T1 晋级：asset_equiv 十类+XAGUSD 腿映射
	{101, migrations.CanonicalFixSQL, false},                  // D-308 勘误：perp/mt5 canonical 回正到 ticks 命名空间
	{102, migrations.XassetExecSQL, false},                    // D-311 XASSET_CARRY 通路：positions CHECK 七值 + universe ct_val
	{103, migrations.IncomeEventsSQL, false},                  // D-334ab 位点①：income_events 实数轨（tranId 三列唯一幂等）
	{104, migrations.XassetNoiseSQL, false},                   // D-335 S1：fin_xasset_monitor noise_bps 真噪声列（24h 摆幅半径）
	{105, migrations.XassetAllowCombosSQL, false},             // D-338 S1：XASSET 白名单表化（播种四行=.env:27 逐字）
	{106, migrations.HarvestCycleSQL, false},                  // D-341 S1：收割线/学习样本锚/exit_kind/v_cycle_realrate 视图
	{107, migrations.CarryUniverseSQL, false},                 // D-336 S2：carry 宇宙扫描快照表（发现层）
	{108, migrations.CarryUniverseNoteSQL, false},             // D-345：suspect 门注记列
	{109, migrations.PaperSpotCarrySQL, false},                // D-350：spot-carry 纸面验证轨
	{110, migrations.PaperBasisSQL, false},                    // D-354：纸面仓盈亏视图（基差采集+两列）
	{111, migrations.VenueAccountSnapshotSQL, false},          // D-358 S1：CEX/MT 账户余额快照（原币 _raw+currency）
	{112, migrations.VenueSnapshotRealOnlySQL, false},         // D-360 S3：快照只留 real（MT demo NOT EXISTS/CEX sim EXISTS 清理）
	{113, migrations.OkxUnifiedCleanupSQL, false},             // D-361 S3：OKX 统一账户旧 spot/perp 双行清场（写侧归一 unified）
	{114, migrations.CexSubaccountSeedsSQL, false},            // D-363 S1：funding/earn 子账户 config 种子（凭据 SELECT 复用/幂等）
	{115, migrations.FinVenueIOSQL, false},                    // D-365 S1：CEX 进出账流水（幂等四元唯一键+ts 倒序索引）
	{116, migrations.XassetComboNotionalSQL, false},           // D-367 S1：组合级 sizing 名义额列（NULL=回退全局 env）
	{117, migrations.AppConfigSQL, false},                     // D-368 S1：运维旋钮 KV（Bucket B 首批，零种子）
	{118, migrations.RadarWatchlistSQL, false},                // D-369 S2：确认信号 watchlist（radar 写/core 读）
	{119, migrations.PaperCfdColumnsSQL, false},               // D-374 S2：纸面轨扩 cfd×perp（structure/mt_venue 两列）
	{120, migrations.PositionEvalsSQL, false},                 // D-377 S1：XASSET 退出评估观测轨（position_evals 窄行表）
	{121, migrations.VenueAccountSnapshotSrcSQL, false},       // D-383 B1/B2：快照行写路标记 src 列（事件 mt_event/okx_event，默认 scheduler）
	{122, migrations.BrokerSymbolMapIsPrimarySQL, false},      // D-383 A2.1/A2.6：is_primary+部分唯一索引+单行组固化+27 组种子
	{123, migrations.RadarDigestStateSQL, false},              // D-389 S1：digest 边沿化集合 hash 状态表
	{124, migrations.RadarDigestMemberSQL, false},             // D-392 S1：digest 成员级边沿（新入才推+flap TTL）
	{125, migrations.CarryGraduationsSQL, false},              // D-403 S1：验证毕业链建议表
	{126, migrations.MarginGuardSQL, false},                   // D-405 S1：snapshot buffer_multiple + fin_venue_margin_status
	{127, migrations.AlertsPositionIDSQL, false},              // D-411 S1：alerts.position_id 持仓级告警列
	{128, migrations.PaperAllInSQL, false},                    // D-410 S1：纸面全账三列+存量费用回填
	{129, migrations.XassetMonitorVenueSQL, false},            // D-423：fin_xasset_monitor cex/mt venue 三元组（存量=binance:perp×3001157:mt5）
	{130, migrations.CashflowsLegDayEventKeySQL, false},       // D-422 R1：腿日唯一索引含 event_key（OPEN费+CLOSE佣金分键）
	{131, migrations.StrategyReasonD424SQL, false},            // D-424：收敛型退出腿序修正归因作废注记
	{132, migrations.DataPlaneVenueSQL, false},                // D-420 S2/S3：fin_cex_depth+油族 CL 映射对齐
	{133, migrations.PaperIntraTradesSQL, false},              // D-425 S1：日内单品高风险轨纸面表
	{134, migrations.AutotradeAllowRulesSQL, false},           // D-443 S1：autotrade venue/canonical 白名单表化+env seed
	{135, migrations.IntraEventsSQL, false},                   // D-441 S2：日内轨栅栏事件表（0134撞D-443改135）
	{136, migrations.CexPrivateEventsSQL, false},              // D-450 S4：CEX 私有 WS 事件审计表
	{137, migrations.SymdigSQL, false},                        // D-464 S1/S2：fin_symbol_aliases + fin_symdig_findings
	{138, migrations.VenueSymbolFlagsSQL, false},              // D-463 S1/S2：fin_venue_symbol_flags + carry_graduations.flag_warn
	{139, migrations.PaperMissStreakSQL, false},               // D-474a S4：paper_spot_carry.miss_streak 缺席独立计数
	{140, migrations.VenueExposureCapsSQL, false},             // D-475a S1+S5：敞口闸表+paper 质量杆观测两列
	{141, migrations.PerpFundingAdmissionSQL, false},          // D-491 S1：PERP_FUNDING 证据准入表（白名单退役）
	{142, migrations.KeydomainConstitutionSQL, false},         // D-496：键域宪法全库归一（venue/venue_type/venue_key generated）
	{143, migrations.CarryGraduationPairColsSQL, false},       // D-498：carry_graduations venue_a/venue_b 源列+venue 展示投影
	{144, migrations.D499TailColsSQL, false},                  // D-499：carry venue_type 拆除+flags 配对域拆列
	{145, migrations.ExecControlBrokerScopeSQL, false},        // D-493：execution_control_events scope_type 补 'broker' 审计域
	{146, migrations.DropBinanceDemoConfigSQL, false},         // D-504：删 binance demo 配置行
	{147, migrations.FactsKindIndexPgstatSQL, false},          // D-508 S3：facts (kind,venue,symbol,ts desc) 索引+pg_stat_statements
	{148, migrations.PositionsLegacyDemoSQL, false},           // D-512 T8：positions.environment NULL→legacy_demo（纯 UPDATE）
	{149, migrations.SettlementIncidentAttributedSQL, false},  // D-512 T9：settlements 事故归因列+存量按同判据回填
	{150, migrations.ExplorerShellSQL, false},                 // D-513 S2：explorer_proposals/explorer_state 记分板骨架双表
	{151, migrations.ConfigToPgSQL, false},                    // D-515：app_secrets+swaparb_allow_combos+exchange enc 三列
	{152, migrations.MacroTablesSQL, false},                   // D-528：macro_event+cot_position+fred_observation 三表
	{153, migrations.RadarWatchlistCanonicalSQL, false},       // D-532 S2：watchlist symbol 方言归一（venues[1] 后缀反推）
	{154, migrations.CalibTablesSQL, false},                   // D-533：calib_scoreboard+calib_params 两表+种值四行
	{155, migrations.IdleFloorStreakSQL, false},               // D-534 B4：睡钱触发层连续天数状态表
	{156, migrations.EndpointFossilSeedSQL, false},            // 化石批 691544b2 落了文件漏注册——D-543 补登（WHERE 守卫幂等，生产零行为变化）
	{157, migrations.ExposureCapsSeedSQL, false},              // D-543：fin_venue_exposure_caps 种子五行（幂等）
	{159, migrations.ExposureCapTruthFixSQL, false},           // D-547：组合帽对齐 2×notional+okx:unified→okx:perp 重键名
	{160, migrations.PaperDualCexColumnsSQL, false},           // D-555 S1：paper/carry_graduations venue_type_a/b+venue_key_a/b 生成列
	{161, migrations.QuoteUnitScalesSQL, false},               // D-594：报价单位归一层（price_scale/qty_scale+铜定标+XCU 别名）
	{162, migrations.PositionFeesNetPnlSQL, false},            // D-599：positions fees_usd/fee_quality（realized_pnl 净额化）
	{163, migrations.StrategyStatsNotifySQL, false},           // D-004 P1#3：disabled 变更 pg_notify（事件源替代 60s 轮询）
	{164, migrations.CanonicalExposureCapsSQL, false},         // D-006 S3：fin_canonical_exposure_caps 聚簇闸表
	{165, migrations.MT5ExposureCap6000SQL, false},            // D-007 S2：MT5 两户 venue 敞口闸 $6000（业主批）
	{166, migrations.MarginSemanticsSQL, false},               // D-010：margin_usd 列+互斥；venue cap 语义翻保证金
	{167, migrations.LegExternalStatusSQL, false},             // D-015 ②：position_legs.status 增 EXTERNAL
	{168, migrations.D013LedgerTruthSQL, false},               // D-013：partial 合法化 + #497–507 账本回填
	{169, migrations.LegFillKindSQL, false},                   // D-018 阶段a：position_legs.fill_kind（maker|taker；168 已被 D-013 占用）
	{170, migrations.BrokerSwapFreeSQL, false},                // D-014 S1：broker_accounts.swap_free 旗标列 + taker_fee_bps（R1：169 撞号改 170）
	{171, migrations.MakerFeeEnableSQL, false},                // D-018 后段：exchange_configs.maker_fee_bps + app_config maker 启用键
	{172, migrations.IncidentExitKindCleanupSQL, false},       // D-032 R1：清零成交 FAILED 误打的 legged_unwind
	{173, migrations.PaperWindowStartSQL, false},              // D-033①：app_config 播种 NOX_PAPER_WINDOW_START
	{174, migrations.DropOpportunitiesTickSQL, false},         // D-040②：opportunities_tick 死表 DROP（零引用，已备份）
	{175, migrations.ProbePositionsSQL, false},                // D-047①：positions +decision_ref/auto_close_at（PROBE 单腿探针）
	{176, migrations.ProbeLLMLoopSQL, false},                  // D-048①②：explorer_proposals 探针提案契约+审批流转列
	{177, migrations.MacroZhResetSQL, false},                  // D-053①：mql5 源 en 旧行清除（zh 端点切换）
	{178, migrations.DropPerpFundingAdmissionSQL, false},      // D-057⑤：PERP_FUNDING 证据准入表退役（已备份）
	{179, migrations.RetireUniverseAlertsSQL, false},          // D-072：universe 发现告警退役——清历史 info 噪音行
	{180, migrations.D120RetireResearchSQL, false},            // D-120：研究面 A 批 13 表幂等 DROP（已备份）
	{181, migrations.FactsNullSymbolIdxSQL, false},            // D-121：facts NULL-symbol 组枚举部分索引（0180 撞号顺延）
	{182, migrations.BinanceEarnSeedSQL, false},               // D-130 R1：binance:earn 凭据复制补种 + binance:funding 行摘除（A 案）
	{183, migrations.DropBinanceEarnSQL, false},               // D-133：binance earn 面整体摘除（counter-migration 收口 0182）
	{184, migrations.DropCnfiRepoSQL, false},                  // D-139：CNFI/逆回购面 13 表幂等 DROP（已备份）
	{185, migrations.FactsKindTsIdxSQL, false},                // D-152 fixup：facts(kind,ts) retention 候选寻址
	{186, migrations.ElimEvidenceSeedSQL, false},              // D-180：统计证据阈值 app_config 播种
	{187, migrations.CexDCIQuoteSQL, false},                   // D-183：三所 DCI/双币产品报价表
	{189, migrations.CapabilityConstraintSQL, false},          // D-186②：能力约束表 fin_capability_constraint
	{190, migrations.ConditionGapSQL, false},                  // D-186③：条件缺口表 fin_condition_gap
	{191, migrations.CapabilityPermissionSQL, false},          // D-188②：权限探测面 fin_capability_permission
	{192, migrations.DCIOrderSQL, false},                      // D-191：DCI 申购幂等账本 fin_dci_order
	{193, migrations.EmployeeRegistrySQL, false},              // D-190：员工花名册 fin_employee_registry
	{194, migrations.ProposalSQL, false},                      // D-189：提案实体化 fin_proposal+dci_order 回链
	{195, migrations.GapResolveNoteSQL, false},                // D-193②：缺口收口备注列
	{196, migrations.OTMBoundSQL, false},                      // D-191-R9：%OTM 闸界入约束表
	{197, migrations.DCIOrderRefSQL, false},                   // D-191-R10：报价行 order_ref 补列
	{198, migrations.DCIAmountBoundSQL, false},                // D-191-R11：报价行 min/max/decimal 三字段
	{199, migrations.DormantDeferredSQL, false},               // D-204：deferred 态+休眠轨种子
	{200, migrations.DCISettleSQL, false},                     // D-207：DCI 交割观察（0200 漏注册教训——本票补挂）
	{201, migrations.LegForensicsSQL, false},                  // D-208：腿级 fail_reason+半平裸奔阈值
	{188, migrations.OppTemplateSQL, false},                   // D-185：机会模板册（fin_opp_template+cond）
	{1000, migrations.BaselineSQL, true},                      // D-214：生产 schema 压平基线（marker，见上方注释）
	{1001, migrations.EmployeeDeptElevenSQL, false},           // D-220：dept 十一部化
	{1002, migrations.EnvKeysNoxSQL, false},                   // D-230：app_config/app_secrets 键名 ARB_→NOX_
	{1003, migrations.DropUnmigratedSQL, false},               // M3 前置①：不搬表族 DROP（§3 裁定 12 表）
	{1004, migrations.NoxRoRoleSQL, false},                    // D-241⑧：鲜装 nox_ro 补造
	{1005, migrations.DeptReportsSQL, false},                  // D-244：fin_dept_reports（部门日报→经理摘要）
	{1006, migrations.M40RuntimeRequestboxSQL, false},         // D-246/D-247：台账降噪+请求箱分流
	{1007, migrations.LLMCallSQL, false},                      // D-246：fin_llm_call 调用账本
	{1008, migrations.OppEvalSQL, false},                      // D-258 M4.1：fin_opp_eval 评估卡
	{1009, migrations.StrategyLifecycleSQL, false},            // D-260 M4.2：fin_strategy 状态机+判据表+提案 SLA
	{1010, migrations.NavBudgetSQL, false},                    // D-264 M4.4：fin_nav_daily+fin_strategy_budget
	{1011, migrations.VenueRejectSQL, false},                  // D-255：fin_venue_reject 败因坑表
	{1012, migrations.ElimHearingSQL, false},                  // D-249①：fin_strategy_hearing 淘汰听证
	{1013, migrations.BinanceEarnReseedSQL, false},            // 财务可见性两件套①：binance:earn 配置行回种
	{1014, migrations.OkxFuturesSQL, false},                   // gap#11：venue_type 枚举放 'futures'+okx:futures 配置行回种
	{1015, migrations.WatchRuleSQL, false},                    // 业主视窗两件套 B：通用盯盘规则表
	{1016, migrations.OppEvalUnevalStreakSQL, false},          // auto 轨僵尸提案②：unevaluable 连拍计数列
	{1017, migrations.GapKindTransferSQL, false},              // 跨 venue 调拨提案：gap kind 放 transfer
	{1018, migrations.RadarFuturesAxisSQL, false},             // 宇宙雷达仪器轴：contract_type/expiry_ts/basis_ann
	{1019, migrations.OppHypothesisSQL, false},                // 新品类自发现 S1：fin_opp_hypothesis 假设行表
	{1020, migrations.PaperDeliverySQL, false},                // 日历基差 P0：paper venue_type 枚举加 futures
	{1021, migrations.ManualPositionSQL, false},               // CMC 手工仓位只读纳管：fin_manual_position
	{1022, migrations.GraduationDecidedBySQL, false},          // 毕业委任审计列：carry_graduations.decided_by
	{1023, migrations.GapKindExecutorSQL, false},              // idle 缺口扫描：gap kind 放 executor
	{1024, migrations.ProposalPendingVenueSQL, false},         // earn 多 venue 化：pending 唯一锚加 venue
	{1025, migrations.GateEarnConfigSQL, false},               // D-523③：gate:earn 配置行回种
	{1026, migrations.NavDailyCcySQL, false},                  // D-522：fin_nav_daily PK 升 (day_start,currency)
	{1027, migrations.DCITailGateSQL, false},                  // D-526：DCI 尾险闸约束行（σ 界 k/tenor_max/名义帽）
	{1028, migrations.D526RetireFlatBoundsSQL, false},         // D-526 返修③：平 OTM 界死行清退
	{1029, migrations.VenueIOTransferWalletSQL, false},        // D-536①：fin_venue_io 内部划转列
	{1030, migrations.PerfBaselineSQL, false},                 // D-535：业绩基线锚+历史行归档
	{1031, migrations.SnapshotKeyTsIdxSQL, false},             // 快照键+时序覆盖索引（ExplorerIdleFunds LATERAL 化）
	{1032, migrations.PaperQtyParamSQL, false},                // D-546：纸面名义额参数化
	{1033, migrations.S2TrialNotionalSQL, false},              // D-548：S2 桥试单名义额参数
	{1034, migrations.GraduationUnqualifiedSQL, false},        // 毕业准入闸漏洞修：status 域补 unqualified/stale
	{1036, migrations.PositionStrategyKeySQL, false},          // 仓位归组键：positions.strategy_key+fin_position_strategy_map 判据表
	{1037, migrations.CanonicalProductClassSQL, false},        // D-558：SWAPARB 配对等价键 fin_canonical_product_class（canonical→产品类）
	{1041, migrations.DecisionOutcomesSQL, false},             // D-561：裁定归因簿 fin_decision_outcomes（30d 归因+连败告警）
	{1038, migrations.GradSizingModeSQL, false},               // D-581：毕业拆层 sizing_mode+funding_pct（pct/system 档腿帽 NULL 活池折算）
	{1039, migrations.ComboSourceSQL, false},                  // D-584：armed 白名单出处约束
	{1040, migrations.PaperFirstGateSQL, false},               // D-593：全模板纸面先行不变量
	{1042, migrations.TemplateAutoexecSQL, false},             // D-598：承接位提案自动执行判据列（autoexec+豁免族种子）
	{1043, migrations.ComboPitGateSQL, false},
	{1044, migrations.ComboKillTriggerSQL, false},        // D-587④：armed 坑签名闸（pit_exempt_d 列）
	{1045, migrations.EarnRotationSQL, false},            // D-612/D-613：earn_rotation 息差轮动模板注册
	{1046, migrations.EarnYieldFanoutReversalSQL, false}, // earn_yield 扇出假账冲正（镜像行对消，禁删史）
	{1047, migrations.NavLivemapFixSQL, false},           // NAV liveMap 指纹注血腐败行清理（分币粒度修配套）
	{1048, migrations.CmcTablesReturnSQL, false},         // CMC Markets 采集表族回归（fin_cmc_* 4 表+装配键种子）
	{1049, migrations.WatchRuleUpgradeSQL, false},        // watch_rule 盯盘域升级（level/message——arb 能力归并）
	{1050, migrations.LiveModeSQL, false},                // D-620：LIVE 授权链断层修（真金单闸）
	{1055, migrations.S2CanonNormSQL, false},             // s2bridge 写入侧 canonical 归一·存量回填（全列齐后跑）
	{1051, migrations.Us500SpcxSQL, false},               // cfd 保底 US500 类 asset_equiv 注册（combo#20 双告警根因）
	{1052, migrations.Spx500BinanceSQL, false},           // cfd 保底订正：US500 幻影类撤销，binance SPCX 双腿归 SPX500 类
	{1053, migrations.TrialTierSQL, false},               // D-625 P0：微额试单轨 tier+cost_model+晋级判据
	{1054, migrations.RegimePrecCfgSQL, false},           // D-625 P2：regime/前提塌判据 app_config 种子
	{1056, migrations.SnapshotLatestExprIdxSQL, false},   // rpc-perf 复审修：快照最新行 LATERAL 探针表达式索引
	{1057, migrations.TrialCostWiringSQL, false},         // P0 续件：positions.combo_id 归因列
	{1058, migrations.SettleReflectionSQL, false},        // settle 单消费者：reflection/entry_expected_cost_bps 列
	{1059, migrations.IncomeIsTransferSQL, false},        // D-630：income_events.is_transfer 划转伪影标
	{1060, migrations.TicksKeyTsIdxSQL, false},           // D-633 §4：ticks 复合索引止血（级联 chunk）
	{1061, migrations.AlertsFanoutPendingSQL, false},     // SLO ⑤：alerts.fanout_pending 列（进程外摄入件扇出判据）
	{1062, migrations.ProposalMissCountSQL, false},       // expire 粘性修：miss_count 列（连 N 拍 miss 才杀）
	{1063, migrations.GradFamilyEvidenceSQL, false},      // 毕业判据三值持久化（family_n/win_rate/lower_bound）       // expire 粘性修：miss_count 列（连 N 拍 miss 才杀）
	{1064, migrations.HoldHoursPriorSQL, false},          // funding_carry hh 输入源修：hold_hours_prior 列+种子168h
	{1065, migrations.EconKbVerdictsSQL, false},          // 经济经验库消化裁定表（canon/dismiss/merge+理由必填）
	{1066, migrations.CgVenueTypeFuturesSQL, false},      // cg venue_type 漂移修：活约束回补 'futures'（基线就地改不回流生产）
	{1067, migrations.VenueTypeFuturesSweepSQL, false},   // venue_type 'futures' 全谱 CHECK 修（D-648——8 表+ticks 族）
	{1068, migrations.AlertReceiptPatternsSQL, false},    // 告警两级语义：回执类前缀判据种子
	{1070, migrations.ExecVenueCapabilitySQL, false},     // D-651：venue 执行面归能力账本投影（1069=symmap-4 支占用避号）
}
