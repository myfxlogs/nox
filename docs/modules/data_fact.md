# internal/data/fact

> 事实截至搬迁 HEAD（M2.1，filter-repo 保史）。判定：决策席末节填。

## 职责
事实层数据模型——facts/ticks 结构与时戳约定。

## 边界
- **上游**：internal/data/adapter, internal/data/collect, internal/data/collect/cmc, internal/data/collect/deribit, internal/data/collect/exchange, internal/data/collect/trx, internal/data/marketstate, internal/data/symdig, internal/research/scan, internal/store/pgstore
- **下游**：（无 internal 依赖）

## 结构
4 个 go 文件（测试 2）。dNNN_ 前缀文件 0（M2.1 入场门已按关注点改名）。

## 测试
- 测试文件 2；依赖 TestDSN/storetest 的 0
- 超限文件：无

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
