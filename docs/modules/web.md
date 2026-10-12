# web —— M2.5b 前端设计稿（D-229②，决策席批后施工）

> 栈：Vite+React 19+TS+纯 CSS（AGENTS §8）；`web/src` 全新写，归档件只读参照禁拷贝。
> 硬约束：零信任（分页/排序/口径全后端）、**唯一数据通道=Connect `StreamUpdates`（server-stream）+ 首载 RPC 补全量**、禁 `setInterval`/本地缓存旧值当现值、每文件 ≤300 行、TS 客户端 `buf generate` 产出（`web/src/gen` 已通）。

## 流订阅单例模型

- `src/stream.ts`：单例 `StreamUpdates` 连接（页面级不各自建连）；按 `response.case` 分发到各页注册 selector；断线由 Connect client 重连，UI 标「连接中」不渲染旧值。
- **case→页面映射**：1 Quote→总览行情条；2 Opportunity→总览机会流；3 Alert→总览/风控告警列表；4 Position→持仓行 upsert；5 SourceHealth→风控健康行；6 Keepalive→连接存活指示；10 FundingNeedNotice→总览弹窗；14 DataRefreshNotice→按 topic 集重取对应 RPC（allow_rules/carry_graduation/paper_carry/finance/trx/funds/pending_entries/cex_scan + **新 topic `employees`**——施工面=常量+roster 心跳写点 `PublishDataRefresh`，proto 零改；未接线时架构页如实显示 beat_ts 不轮询补）；15 BrokerPositionEvent→持仓/账户 delta；16 AccountEvent→账户 delta。
- `GetFrontConfig` 首载拉前端参数；`Health` 仅作连通自检（不入业务渲染）。

## 页面清单 × RPC × 数据流

| 页 | 路由 | 消费 RPC（首载+写） | StreamUpdates case | 说明 |
|---|---|---|---|---|
| 登录页 | `/login` | —（Bearer token 校验走 interceptor） | — | **NOX 露面①**（标题/登录卡品牌） |
| 总览 | `/` | ListAccounts, ListPositions, ListAlerts, ListOpportunities, GetFinanceOverview, ListUnacked | 1,2,3,4,5,10,15,16,14(funds/finance) | 运营仪表盘：净值/持仓聚合/告警/机会流/资金提醒 |
| 持仓 | `/positions` | ListPositions, ListBrokerPositions, CloseBrokerPosition, CloseBrokerPositions | 4,15,14(finance) | 行级 upsert+平仓写径（运维操作 D-213 钦定留） |
| 账户管理 | `/accounts` | 券商组 21 RPC：ListBrokerAccounts, VerifyAccount, UpdateBrokerCredentials, SetBrokerDisabled/Alias/IsReal/Commission/Website, AddBroker/RemoveBroker, ListExchanges, AddExchange, RemoveExchange, SetExchangeLabel/Website, BrokerSymbols, SearchBroker, AutoMapBroker, ListAccounts | 16,3 | 删号/凭据/alias/手续费——写径同 RPC |
| 风控健康 | `/risk` | ListSourceHealth, ListAlerts, ListAccountMarginStatus, ListVerificationStats, GetCapitalCoverage | 3,5,10 | 数据源健康+保证金状态+覆盖率+告警 |
| 公司架构 | `/org` | ListEmployees | 14(employees，待接线) | **D-220 钦定**：十一部树×名册×在线状态（status 后端算定：在职\|滞跳\|离线\|离职），零前端计算 |
| 投资人页 v0 | `/investor` | ListDailyIncome, GetPnlAnchor, ListPositions（只读摘要）, GetFinanceOverview | 6 | **NOX 露面②**（页抬头）；只读 NAV/收益/持仓摘要，无写径无运维件 |
| 报告落款 | — | — | — | **NOX 露面③**：GenerateKbReport/复盘报告渲染落款品牌位（报告页 v2 回补，本票先留品牌常量） |

## 组件拆分（≤300 行/文件）

- `src/main.tsx`：路由+鉴权门（token 存 sessionStorage，未过→`/login`）。
- `src/stream.ts`：单例连接+case 分发+订阅注册表。
- `src/api.ts`：gen client 封装（Authorization 头注入+错误统一）。
- `src/pages/{login,overview,positions,accounts,risk,org,investor}.tsx`：每页一文件，页面内子组件同文件聚合或拆 `src/components/<域>.tsx`；>280 行即拆。
- `src/nox.ts`：品牌常量（名/落款串）——露面点唯一改口处。
- `web/dist` 不入 git；`//go:embed web/dist` 由 `cmd/core` M2.6 装配。

## 砍页举证（归档页→v1 取舍）

- **保留**：Overview→总览、Brokers→账户管理、Positions→持仓、Login→登录。
- **新增**：风控健康（source_health 既有面，决策席看板核心）、公司架构（D-220 钦定）。
- **v1 砍（v2 逐页回补须举证）**：CexScan/CexFinance/Finance/Trx/Knowledge/Reports/Settings/Watchlist/Proposals/Graduation/XAsset/白名单/资金图等专项页——业主主接口=飞书（D-033），v1 只保留「看+运维决策」最小面；专项页数据面（RPC+case）已在 M2.5 留仓，回补=纯前端件。
- **Settings 页内运维写径**（app_config/kill/凭据）v1 不入前端——继续走飞书/psql，避免双写面。

## 禁区对拍

不新增 RPC/proto（D-229①）；不轮询；不搬归档组件；部门级健康聚合口径在后端 `ListEmployees` 响应内消费不自行算；投资人页零写径零敏感运维件。
