# internal/risk/position

> 事实截至搬迁 HEAD（M2.3，filter-repo 保史）。判定：决策席末节填。

## 职责
持仓监控——position 盯盘与 kick 刷新。

## 边界
- **上游**：internal/trading/autotrade
- **下游**：internal/data/bus, internal/platform/safego, internal/review/verification, internal/store/pgstore

## 结构
6 个 go 文件（测试 3）。dNNN_ 前缀文件 0（M2.3 入场门已按关注点改名）。

## 测试
- 测试文件 3；依赖 TestDSN/storetest 的若干
- 无超限非测试文件

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
