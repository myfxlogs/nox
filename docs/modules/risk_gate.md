# internal/risk/gate

> 事实截至搬迁 HEAD（M2.3，filter-repo 保史）。判定：决策席末节填。

## 职责
风控闸——入场/组合/保证金/宏观断闸判定（autotrade 拆搬 gate 面+DCI 闸纯移）。

## 边界
- **上游**：internal/trading/autotrade, internal/trading/dciexec
- **下游**：internal/data/bus, internal/data/fact, internal/data/keydomain, internal/data/macro, internal/platform/combo, internal/platform/errclass, internal/platform/usdnotional, internal/portfolio/capitalview, internal/store/pgstore, internal/trading/exec

## 结构
39 个 go 文件（测试 20）。dNNN_ 前缀文件 0（M2.3 入场门已按关注点改名）。

## 测试
- 测试文件 20；依赖 TestDSN/storetest 的若干
- 无超限非测试文件

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |

## D-620（2026-10-18）LIVE 真金单闸
- mode 枚举放开 LIVE（迁移 1050）；`live_gates` active 行=唯一授权记录（部分唯一索引 WHERE revoked_at IS NULL——撤权可续权）。
- 唯一授权径=`ControlPlane.GrantLive`（前置 env=='live'+readiness Tradable；单事务撤旧+插新+mode=LIVE+事件）。`SetMode("LIVE")`=错。
- 降级/TriggerKillSwitch 先撤 live_gates（kill 覆盖 DCI/earn 提案径——GoProposal 首步 ModeGate 三拒）。
- `Gate.Submit`/`ExecuteOpportunityInternal`/meanrevert/probe：mode≠LIVE 拒 is_real 腿；gateChecker nil=fail-closed FAILED（展示径 KillSwitch=true）。
- `NOX_LIVE_TRADE` env 键退役；RPC 写口=SetExecutionMode/GetExecutionMode；selfwatch 小时拍 <24h warn/过期 critical。
