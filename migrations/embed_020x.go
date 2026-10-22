package migrations

import _ "embed"

//go:embed replay/0200_dci_settle.sql
var DCISettleSQL string // D-207：DCI 交割观察（settled 态+settle_* 四列）

//go:embed replay/0201_leg_forensics.sql
var LegForensicsSQL string // D-208：position_legs.fail_reason+半平裸奔阈值种子

//go:embed 1001_employee_dept_eleven.sql
var EmployeeDeptElevenSQL string // D-220/D-225⑤：dept 八标签→十一部（采集/链上→数据、执行→交易、通知→IR）

//go:embed 1002_env_keys_nox.sql
var EnvKeysNoxSQL string // D-230：app_config/app_secrets 键名 ARB_→NOX_（改名原子票）

//go:embed 1003_drop_unmigrated.sql
var DropUnmigratedSQL string // M3 前置①：不搬表族 DROP（§3 裁定 12 表；切流先 pg_dump 归档）

//go:embed 1004_nox_ro_role.sql
var NoxRoRoleSQL string // D-241⑧：鲜装 nox_ro 补造（0041+0047 语义合并；生产切流走 ALTER RENAME）

//go:embed 1005_dept_reports.sql
var DeptReportsSQL string // D-244：fin_dept_reports（十一部部报+账本专项+经理摘要）

//go:embed 1006_m40_runtime_requestbox.sql
var M40RuntimeRequestboxSQL string // D-246/D-247 M4.0：台账降噪（未登记/已剔族清行+dept 七值）+请求箱 kind 白名单+runtime 转票

//go:embed 1007_llm_call.sql
var LLMCallSQL string // D-246 v3 §4.4：fin_llm_call LLM 调用账本（费用入 D-013 口径）

//go:embed 1008_opp_eval.sql
var OppEvalSQL string // D-258 M4.1：fin_opp_eval 评估卡（一提案一卡）

//go:embed 1009_strategy_lifecycle.sql
var StrategyLifecycleSQL string // D-260 M4.2：fin_strategy 状态机+fin_strategy_criteria 判据表+提案 SLA 键

//go:embed 1010_nav_budget.sql
var NavBudgetSQL string // D-264 M4.4：fin_nav_daily 日切 NAV + fin_strategy_budget 策略预算表

//go:embed 1011_venue_reject.sql
var VenueRejectSQL string // D-255：fin_venue_reject 败因坑表

//go:embed 1012_elim_hearing.sql
var ElimHearingSQL string // D-249①：fin_strategy_hearing 淘汰多空听证记录

//go:embed 1013_binance_earn_reseed.sql
var BinanceEarnReseedSQL string // 财务可见性两件套①：binance:earn 配置行回种（D-285 分币口径解除 D-133 约束）

//go:embed 1014_okx_futures.sql
var OkxFuturesSQL string // gap#11：venue_type 枚举放 'futures'+okx:futures 配置行回种

//go:embed 1015_watch_rule.sql
var WatchRuleSQL string // 业主视窗两件套 B：通用盯盘规则表

//go:embed 1016_opp_eval_uneval_streak.sql
var OppEvalUnevalStreakSQL string // auto 轨僵尸提案②：unevaluable 连拍计数列

//go:embed 1017_gap_kind_transfer.sql
var GapKindTransferSQL string // 跨 venue 调拨提案：gap kind 放 'transfer'

//go:embed 1018_radar_futures_axis.sql
var RadarFuturesAxisSQL string // 宇宙雷达仪器轴：contract_type/expiry_ts/basis_ann

//go:embed 1019_opp_hypothesis.sql
var OppHypothesisSQL string // 新品类假设行表

//go:embed 1020_paper_delivery.sql
var PaperDeliverySQL string // 日历基差 P0 纸面轨：venue_type 枚举加 futures

//go:embed 1021_manual_position.sql
var ManualPositionSQL string // CMC 手工仓位只读纳管：fin_manual_position

//go:embed 1022_graduation_decided_by.sql
var GraduationDecidedBySQL string // 毕业委任审计列：carry_graduations.decided_by

//go:embed 1023_gap_kind_executor.sql
var GapKindExecutorSQL string // okx idle 缺口：fin_condition_gap kind 放 executor

//go:embed 1024_proposal_pending_venue.sql
var ProposalPendingVenueSQL string // earn 多 venue 化：pending 唯一锚加 venue

//go:embed 1025_gate_earn_config.sql
var GateEarnConfigSQL string // D-523③：gate:earn 子账户配置回种

//go:embed 1026_nav_daily_ccy.sql
var NavDailyCcySQL string // D-522：fin_nav_daily PK 升 (day_start,currency)

//go:embed replay/1027_dci_tail_gate.sql
var DCITailGateSQL string // D-526：DCI 尾险闸约束行（σ 界 k/tenor_max/名义帽）

//go:embed replay/1028_d526_retire_flat_bounds.sql
var D526RetireFlatBoundsSQL string // D-526 返修③：平 OTM 界死行清退

//go:embed replay/1029_venue_io_transfer_wallet.sql
var VenueIOTransferWalletSQL string // D-536①：fin_venue_io 划转列（from/to_wallet）

//go:embed replay/1030_perf_baseline.sql
var PerfBaselineSQL string // D-535：业绩基线锚+历史行归档（nav_daily/income_events archived）

//go:embed replay/1031_snapshot_key_ts_idx.sql
var SnapshotKeyTsIdxSQL string // 快照键+时序覆盖索引（每键最新行 LATERAL 探针化）

//go:embed replay/1032_paper_qty_param.sql
var PaperQtyParamSQL string // D-546：纸面名义额参数化（app_config paper_qty_usd）

//go:embed replay/1033_s2_trial_notional.sql
var S2TrialNotionalSQL string // D-548：S2 桥试单名义额参数化（app_config s2_trial_notional_usd）

//go:embed 1034_graduation_unqualified.sql
var GraduationUnqualifiedSQL string // 毕业准入闸漏洞修：status 域补 unqualified/stale

//go:embed 1036_position_strategy_key.sql
var PositionStrategyKeySQL string // 仓位归组键：positions.strategy_key+判据表

//go:embed 1037_canonical_product_class.sql
var CanonicalProductClassSQL string // D-558：SWAPARB 配对等价键表（canonical→产品类，111 行种子）

//go:embed 1041_decision_outcomes.sql
var DecisionOutcomesSQL string // D-561：裁定归因簿（真金裁定→30d 结算归因+连败告警）

//go:embed 1038_grad_sizing_mode.sql
var GradSizingModeSQL string // D-581：毕业批准拆层 sizing_mode 三态（fixed/pct/system）+funding_pct 原样落表

//go:embed 1039_combo_source.sql
var ComboSourceSQL string // D-584：armed 白名单出处约束（source CHECK+provenance_id+enable 闸）

//go:embed 1040_paper_first_gate.sql
var PaperFirstGateSQL string // D-593：全模板纸面先行不变量（payoff_class+paper_exempt_d 列）

//go:embed 1042_template_autoexec.sql
var TemplateAutoexecSQL string // D-598：承接位提案自动执行判据列（autoexec+豁免族种子）

//go:embed 1043_combo_pitgate.sql
var ComboPitGateSQL string // D-587④：armed 坑签名闸（pit_exempt_d 豁免引用列）

//go:embed 1044_combo_kill_trigger.sql
var ComboKillTriggerSQL string // kill 扳机判据列（net_pnl+连败双闸 NULL 默认） // D-587④：armed 坑签名闸（pit_exempt_d 豁免引用列）

//go:embed 1045_earn_rotation.sql
var EarnRotationSQL string // D-612/D-613：earn_rotation 模板+豁免+autoexec+fin_strategy live

//go:embed 1046_earn_yield_fanout_reversal.sql
var EarnYieldFanoutReversalSQL string // earn_yield 扇出假账冲正（镜像行对消，禁删史）

//go:embed 1047_nav_livemap_fix.sql
var NavLivemapFixSQL string // NAV liveMap 指纹注血腐败行清理（分币粒度修配套）

//go:embed 1048_cmc_tables.sql
var CmcTablesReturnSQL string // CMC Markets 采集表族回归（M3 剔除后 TRX 页三区业主令复工）
//go:embed 1049_watch_rule_upgrade.sql
var WatchRuleUpgradeSQL string // watch_rule 盯盘域升级（level/message——arb price_alerts 能力归并，1003 裁废域不复活）

//go:embed 1050_live_mode.sql
var LiveModeSQL string // D-620：LIVE 授权链（mode 枚举+NOX_LIVE_TRADE 退役+live_gates active 部分唯一索引）

//go:embed 1051_us500_spcx.sql
var Us500SpcxSQL string // cfd 保底 US500 类 asset_equiv 注册（combo#20 簿缺席+universe 缺席根因）

//go:embed 1052_spx500_binance.sql
var Spx500BinanceSQL string // cfd 保底订正：US500 幻影类撤销，binance SPCX 双腿归 SPX500 类
//go:embed 1053_trial_tier.sql
var TrialTierSQL string // D-625 P0：微额试单轨 tier+cost_model+晋级判据种子
//go:embed 1054_regime_prec_cfg.sql
var RegimePrecCfgSQL string // D-625 P2：regime/前提塌判据 app_config 种子（禁硬编码）

//go:embed 1055_s2canon_norm.sql
var S2CanonNormSQL string // s2bridge 写入侧 canonical 归一·存量回填
//go:embed 1056_snapshot_latest_expr_idx.sql
var SnapshotLatestExprIdxSQL string // LatestVenueAccountSnapshots LATERAL 探针表达式索引
//go:embed 1057_trial_cost_wiring.sql
var TrialCostWiringSQL string // P0 续件：positions.combo_id 归因列
//go:embed 1058_settle_reflection.sql
var SettleReflectionSQL string // settle 单消费者：reflection+entry_expected_cost_bps 列
//go:embed 1059_income_is_transfer.sql
var IncomeIsTransferSQL string // D-630：income_events.is_transfer 划转伪影标

//go:embed 1060_ticks_key_ts_idx.sql
var TicksKeyTsIdxSQL string // D-633 §4：ticks (venue,symbol,venue_type,ts DESC) 复合索引止血

//go:embed 1061_alerts_fanout_pending.sql
var AlertsFanoutPendingSQL string // SLO ⑤：alerts.fanout_pending 列（进程外摄入件扇出判据）

//go:embed 1062_proposal_miss_count.sql
var ProposalMissCountSQL string // expire 粘性修：fin_proposal.miss_count 列

//go:embed 1063_grad_family_evidence.sql
var GradFamilyEvidenceSQL string // 毕业判据三值持久化列

//go:embed 1064_hold_hours_prior.sql
var HoldHoursPriorSQL string // funding_carry hh 输入源修：模板持有先验列（D-642 发现一病灶）

//go:embed 1065_econ_kb_verdicts.sql
var EconKbVerdictsSQL string // 经济经验库消化裁定表（canon/dismiss/merge+理由必填）

//go:embed 1066_cg_venue_type_futures.sql
var CgVenueTypeFuturesSQL string // carry_graduations venue_type 漂移修：'futures' 回补（基线就地改不回流生产）

//go:embed 1067_venue_type_futures_sweep.sql
var VenueTypeFuturesSweepSQL string // venue_type 'futures' 全谱 CHECK 修（D-648——8 表+ ticks 族，broker_accounts MT 域不涉）

//go:embed 1068_alert_receipt_patterns.sql
var AlertReceiptPatternsSQL string // 告警两级语义：回执类前缀判据 app_config 种子

//go:embed 1069_gap_capital_marked.sql
var GapCapitalMarkedSQL string // D-652-C：capital 回执核验环（marked 态+核验锚三列）

//go:embed 1070_exec_venue_capability.sql
var ExecVenueCapabilitySQL string // D-651 A 段：venue 执行面归能力账本投影种子

//go:embed 1071_personal_positions_venue.sql
var PersonalPositionsVenueSQL string // STATE 行49：venue 列转正（note 词解析退役）

//go:embed 1072_monitor_pair_canon.sql
var MonitorPairCanonSQL string // monitor 簿 pair 键归一·存量回填（S2 方言 canonical 病灶）

//go:embed 1073_employee_event_driven.sql
var EmployeeEventDrivenSQL string // 员工台账事件驱动分型（任务型 spawn 不判离线）
