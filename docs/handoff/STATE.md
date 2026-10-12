# STATE — 交接现状

## 现状

- **2026-10-12 D-212 重建中**：本仓=新仓（module `arb`，`/opt/arb`）；旧仓=`/opt/arb-archive`（hotfix-only，其 STATE 全史在 `/opt/arb-archive/docs/handoff/STATE.md`，只读）。归档仓 hotfix 行「待移植」清单记于此段下方，M3 切流前清零。
- 待移植：无。

## 票

| 票 | 状态 | owner | 备注 |
|---|---|---|---|
| M0 归档与清场 | ✅ | 复审席 | tag `archive-2026-10-12` 双在；`/opt/arb-archive` build 绿；未合并分支 7 条保留待裁 |
| M1 骨架 | 领 | 实施席 | 本票 |
| M2.1 数据部 | 待领（前置 M1） | 实施席 | §1 数据部各包+wire+proto 数据面；出口=包测试全绿 |
| M2.2 账本部+复盘部 | 待领 | 实施席 | settlement/settleadapter/swapledger/reconcile/review/mmm/sysretro/verification/kbreport |
| M2.3 交易部+风控部 | 待领 | 实施席 | exec/engine/dciexec/autotrade 拆搬/position/selfwatch/gate；dciexec 闸调用点改经 risk 单入口（语义不变）；出口+race 净 |
| M2.4 研究部+投资部+运营部 | 待领 | 实施席 | scan(剔 intra)/detector/evaluator/econ(+spread)/oppmatch/explorer/capitalview/idle/trxvote/lifecycle(拆入)/能力账本投影 |
| M2.5 API+IR+前端 | 待领 | 实施席 | dashboard 拆搬/auth/notify/inboxfmt/web 9 tab/proto 删 CMC·pricealert 组；web 绿、RPC 面对账 |
| M2.6 装配与员工 | 待领 | 实施席 | cmd/core 33 wire、roster Dept 十一部、D-211 钩子前移、tools 7、cron 脚本；`ListEmployees` 全员 |
| M3 切流 | 待领（前置 M2.x 全 ✅） | 复审席 | 搬迁清单 §7 对等闸+§8 切流；生产跑新仓 |
| M4+ 新件 | 待领 | 按架构稿 §10 | 复盘员→生命周期+晋级闸→验证器+评估卡→风控单入口收口→NAV+配置器→投资人页 |

> 票文权威出处=`docs/搬迁清单.md` §9；各票范围/禁区/验收全文以该表+归档仓 STATE 行（M0/M1）为准。
