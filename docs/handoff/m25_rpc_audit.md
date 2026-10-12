# M2.5 RPC 审计面（D-225④，M2.5R1 补）

> 事实截至 wip/m25-api。proto `service.proto` RPC 数：归档 115 → 新仓 107。
> 消费面口径：今日唯一 RPC 客户端=前端（M2.5b 待写）+ cmd/core Connect 挂载（M2.6 wire）；
> 飞书查询/ops 工具走 pgstore 直读（`feishu_query_store.go` 适配器），不经 ConnectRPC——
> 留存面正当性=看板服务整体搬入（D-225① 整搬裁定），逐 RPC 分组列于下。

## 剔除面（8，逐条举证）

| RPC | 剔除理由 |
|---|---|
| CreatePriceAlertRule / DeletePriceAlertRule / ListPriceAlertRules / UpdatePriceAlertRule | price_alerts 表不搬（D-212 §3），CRUD 四件全族死 |
| GetHedgeStructure / ListHedgeFlow | 对冲=CMC 域（fin_cmc_* baseline 不存，D-225② 字段级证据） |
| ListIntraTrades / IntraTrackStats | paper_intra_trades/intra_events 不搬（D-212 §3），INTRA 沙箱退役域孤儿 |

剔除后全仓 Go/TS 零引用（gen 随 buf regen 同步）。

## 留存面 107 RPC × 分组消费者

| 组 | 数 | RPC | 消费面 |
|---|---|---|---|
| 券商/账户 | 21 | ListBrokerPositions, CloseBrokerPosition, CloseBrokerPositions, ListAccounts, SearchBroker, AddBroker, RemoveBroker, UpdateBrokerCredentials, ListBrokerAccounts, VerifyAccount, ListExchanges, AddExchange, RemoveExchange, SetExchangeLabel, SetExchangeWebsite, BrokerSymbols, SetBrokerAccountAlias, SetBrokerAccountIsReal, SetBrokerDisabled, SetBrokerCommission, SetBrokerWebsite | 前端券商页+运维操作（删号/凭据/alias/手续费/禁用——D-213「运维操作全留」） |
| 快照/告警面 | 11 | ListLatestQuotes, ListLatestFacts, ListOpportunities, ListAlerts, ListSourceHealth, ListFacts, AckAlert, AckAll, ListUnacked, DeleteAlerts, ClearAckedAlerts | 前端总览/告警页；Ack 同源飞书侧 pgstore AckAll |
| 持仓/收益 | 5 | ListPositions, AddDailyIncome, ListDailyIncome, GetPnlAnchor, ResetPnlAnchor | 前端持仓页+收益台账 |
| 探针审批 | 5 | ListProbes, ListProbeProposals, OpenProbe, ApproveProbe, RejectProbe | 前端+飞书审批（D-213 探针审批钦定留） |
| TRX 面 | 3 | TrxAccountRefresh, GetTrxYieldRadar, GetTrxYieldCurve | 前端 TRX 卡+收益曲线 |
| xasset 组合 | 4 | ListXAssetAllowCombos, UpsertXAssetAllowCombo, ResumeXAssetCombo, PreviewXAssetComboRisk | 前端 XASSET 组合管理页 |
| appconfig/secrets | 5 | ListAppConfig, UpdateAppConfig, SelfRestart, ListAppSecrets, UpdateAppSecret | 运维操作（app_config 写/SelfRestart——D-213 钦定留） |
| 执行 | 2 | ExecuteOpportunity, ClosePosition | 前端手动执行/平仓（D-213 平仓钦定留） |
| 提案 | 2 | ListProposals, AckProposal | 前端提案页+飞书 ack（D-213 钦定留） |
| 流/杂面 | 4 | StreamUpdates, Health, GetFrontConfig, GetStrategyLedger | 前端唯一数据源（StreamUpdates 钦定留） |
| analyst/AI | 4 | AskAnalyst, AppendAnalystNote, GetAiSettings, SetAiSettings | 前端分析师页+AI 配置 |
| 报告/复盘 | 4 | GenerateRetroReport, ListReports, GetReport, GenerateKbReport | 前端复盘页 |
| 券商补充 | 7 | AutoMapBroker, ListSwapCombos, ListSymbolMappings, SaveSymbolMapping, DeleteSymbolMapping, ListMappingSuggestions, ListCanonicals | 前端符号映射/组合页 |
| 名单/规则 | 4 | ListAutotradeAllowRules, SetAutotradeAllowRuleEnabled, AddAutotradeAllowRule, DeleteAutotradeAllowRule | 前端开仓白名单页 |
| 资金图 | 2 | GetCapitalCoverage, GetCapitalMap | 前端资金覆盖/资金图页 |
| 毕业链 | 3 | ListCarryGraduations, ApproveCarryGraduation, RejectCarryGraduation | 前端毕业审批页 |
| DCI 面 | 2 | ListPersonalPositions, ListDCIOrders | 前端 DCI 页；飞书 fund 查询同源 pgstore ListDCIOrders |
| 其余单件 | 19 | BackfillSwapHistory, ListXAssetPendingEntries, ListPaperSpotCarry, GetFinanceOverview, GetCexFinance, GetCexScanSnapshot, ListEmployees, ListKnowledgeEntries, ListKBEntries, GetKBEntry, ListVenueExposureCaps, SetVenueExposureCap, ListVerificationStats, ExecuteSpotCarryDemo, ListSpotCarryDemo, GetWatchlist, SetWatchlist, ListAccountMarginStatus, ListMatrixUniverse | 前端各专项页+ListEmployees（D-220 架构页钦定）+运维（Backfill/VenueExposure 写） |

合计 8 删 + 107 留 = 115 对拍闭合。

## 消费面备注

- `StreamUpdates`：前端唯一数据通道（AGENTS §5 实时性条款），保留全部域帧（hedge case17/cmc_update 帧随族件剔除、字段号 reserved 留号）。
- 飞书侧不经 RPC：`feishu_query_*` 经 `PgStoreQueryAdapter` 直读 pgstore（AckAll/ListDCIOrders/Proposals 同源语义，非 RPC 消费）。
- cmd/core 装配（M2.6）整挂 `DashboardServiceHandler`——107 面即挂载面。
