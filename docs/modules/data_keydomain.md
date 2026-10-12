# internal/data/keydomain

> 事实截至搬迁 HEAD（M2.1，filter-repo 保史）。判定：决策席末节填。

## 职责
venue 键域——venueKey/canonical 解析与 BareOf 归一。

## 边界
- **上游**：internal/data/adapter, internal/data/bus, internal/data/cexscan, internal/data/collect/exchange, internal/data/marketstate, internal/platform/errclass, internal/platform/notional, internal/research/scan, internal/review/verification, internal/store/pgstore
- **下游**：（无 internal 依赖）

## 结构
1 个 go 文件（测试 0）。dNNN_ 前缀文件 0（M2.1 入场门已按关注点改名）。

## 测试
- 测试文件 0；依赖 TestDSN/storetest 的 0
- 超限文件：无

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
