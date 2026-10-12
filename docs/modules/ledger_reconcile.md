# internal/ledger/reconcile

> 事实截至搬迁 HEAD（M2.3，filter-repo 保史）。判定：决策席末节填。

## 职责
对账平仓——venue 实仓与库仓对拍/僵尸仓收口/auto_close。

## 边界
- **上游**：internal/portfolio/capitalview
- **下游**：internal/data/adapter, internal/data/adapter/gen/mtapi/mt5, internal/data/keydomain, internal/data/venue, internal/gen/dashboardv1, internal/platform/httpx, internal/platform/signutil, internal/store/pgstore, internal/trading/exec

## 结构
59 个 go 文件（测试 32）。dNNN_ 前缀文件 0（M2.3 入场门已按关注点改名）。

## 测试
- 测试文件 32；依赖 TestDSN/storetest 的若干
- 无超限非测试文件

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
