# internal/portfolio/idle

> 事实截至搬迁 HEAD（M2.4，filter-repo 保史）。判定：决策席末节填。

## 职责
闲置资金监控——睡钱触发层（连破地板 N 天告警+提案，原 sleepmon）。

## 边界
- **上游**：无（cmd 装配点待 M2.6）
- **下游**：internal/ir/inboxfmt, internal/store/pgstore

## 结构
2 个 go 文件（测试 1）。dNNN_ 前缀文件 0（M2.4 入场门已按关注点改名）。

## 测试
- 测试文件 1
- 无超限非测试文件

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
