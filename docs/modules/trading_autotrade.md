# internal/trading/autotrade

> 事实截至搬迁 HEAD（M2.3，filter-repo 保史）。判定：决策席末节填。

## 职责
自动交易引擎——机会评估→意图产出→持仓监控→退出判定管线。

## 边界
- **上游**：无（cmd 装配点待 M2.6）
- **下游**：internal/data/bus, internal/data/fact, internal/data/isreal, internal/data/keydomain, internal/data/macro, internal/data/marketstate, internal/ledger/settlement, internal/platform/combo, internal/platform/errclass, internal/platform/safego, internal/platform/usdnotional, internal/portfolio/capitalview, internal/portfolio/lifecycle, internal/research/econ, internal/review/mmm, internal/risk/gate, internal/risk/position, internal/store/pgstore, internal/trading/exec

## 结构
121 个 go 文件（测试 71）。dNNN_ 前缀文件 0（M2.3 入场门已按关注点改名）。

## 测试
- 测试文件 71；依赖 TestDSN/storetest 的若干
- 无超限非测试文件

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
