# internal/platform/usdnotional

> 事实截至搬迁 HEAD（M2.1，filter-repo 保史）。判定：决策席末节填。

## 职责
USD 名义额折算。

## 边界
- **上游**：—（本票消费者后续 M2.x 挂入）
- **下游**：internal/data/bus

## 结构
3 个 go 文件（测试 2）。dNNN_ 前缀文件 0（M2.1 入场门已按关注点改名）。

## 测试
- 测试文件 2；依赖 TestDSN/storetest 的 0
- 超限文件：无

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
