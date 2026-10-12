# internal/portfolio/trxvote

> 事实截至搬迁 HEAD（M2.4，filter-repo 保史）。判定：决策席末节填。

## 职责
TRX 托管资产收益管理——投票/再平衡执行面（账本口径不入 8%，D-066）。

## 边界
- **上游**：无（cmd 装配点待 M2.6）
- **下游**：internal/platform/safego, internal/store/pgstore

## 结构
15 个 go 文件（测试 7）。dNNN_ 前缀文件 0（M2.4 入场门已按关注点改名）。

## 测试
- 测试文件 7
- 无超限非测试文件

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
