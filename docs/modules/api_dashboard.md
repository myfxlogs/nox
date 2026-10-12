# internal/api/dashboard

> 事实截至搬迁 HEAD（M2.5，filter-repo 保史；4 件 graduation 文件经 merge-commit 内容损失回补，见观察）。判定：决策席末节填。

## 职责
ConnectRPC 看板服务——`dashboardv1` 107 RPC 全部 handler + StreamUpdates 服务端流 + broker/账户监控（持仓面查询只读，交易写径在 trading/exec）。

## 边界
- **上游**：—（装配点 cmd/core wire_* 归 M2.6）；autotrade 经 `engineService` 窄接口反向注入（D-508）
- **下游**：internal/gen/dashboardv1（ConnectRPC handler）、internal/store/pgstore、internal/data/{bus,fact,venue,keydomain,marketstate,adapter,collect/exchange}、internal/trading/exec、internal/ledger/reconcile、internal/research/{econ,scan}、internal/portfolio/{capitalview,trxvote}、internal/review/{kbreport,sysretro,verification}、internal/infra/dephealth、internal/platform/*（alertcool/analyst/combo/config/cryptoutil/errclass/httpx/notional/safego/usdnotional）
- **不负责**：执行决策（trading）、账本写径（ledger）、飞书投递（ir/notify）

## 结构
343 个 go 文件（测试 179），~50k LOC。按关注点：service_* RPC handler 群 / account_monitor+broker_* 监控 / position_monitor+stream 服务端流 / trx_* 波场合约面 / graduation_* 毕业链 / capital_* 资金图 / kb+analyst+appconfig 杂面。
**D-225② 族件剔除**：service_cmc_watch*/cmc_equity_alert*/price_alert*/service_hedge* 全族 + service.go/setters cmc 字段 + proto PriceAlertRule×4/hedge×2/CMCWatchUpdate/INTRA×2 RPC 及孤儿消息删除（buf regen）。

## 入口与导出面
- `New(store, qbus, pipeline, bm, reviewer, …)`（service_ctor.go:51）——21 参构造
- `Service` 实现全部 `dashboardv1.DashboardServiceHandler`；`StreamUpdates` server-stream
- `alertInserter` 局部接口（alert_inserter.go）——probe/健康件告警落库

## 运行时行为
- posMon/accountMon/brokerFeed 订阅 quote/account 事件流；StreamUpdates 按 case 号扇出各域帧
- hedge(case17)/cmc_update stream 载荷随族件剔除拆除（stream.proto reserved 留号）

## 测试
- 179 测试文件；大量依赖 TestDSN/storetest（5434）；-race 专项（account_monitor_d460_concurrency 等）

## 观察（只记事实）
- **merge-commit 内容损失回补**：filter-repo 未保 merge commit 冲突消解改动，4 件 graduation 文件（service_carry_graduation/service_graduation_entry/service_graduation_store/service_carry_graduation_helpers）按归档 master 版取回+import 改写；`graduationStore` 接口补 `ListAssetEquiv`；resolver 调用对齐现行 6 参签名（symbol/mtVenue/cexVenue/platform/symbolMappings/assetEquiv）
- CMC 存储面整体剔除（pgstore/cmc.go+cmc_query.go+hedge_flow.go；fin_cmc_* 表自 baseline 起不存在）；trx_yield 双成分化、feishu query CMC 工具移除、capital coverage/trx APR 存活面改读留存数据源
- 超 300 行文件以测试文件为主（mt_health_monitor_test 1049 行等）；check-lines 软警告随搬入面
- 搬迁纯移动+import 机械改写；语义改动仅限 D-225 剔除面与上述回补/适配

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
