# internal/risk/selfwatch

> 事实截至搬迁 HEAD（M2.3，filter-repo 保史）。判定：决策席末节填。

## 职责
系统自盯——selfwatch 巡检/阶段报告。

## 边界
- **上游**：无（cmd 装配点待 M2.6）
- **下游**：internal/data/fact, internal/data/macro, internal/platform/safego, internal/research/scan, internal/review/sysretro, internal/store/pgstore

## 结构
18 个 go 文件（测试 8）。dNNN_ 前缀文件 0（M2.3 入场门已按关注点改名）。

## 测试
- 测试文件 8；依赖 TestDSN/storetest 的若干
- 无超限非测试文件

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
