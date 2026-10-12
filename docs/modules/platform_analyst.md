# internal/platform/analyst

> 事实截至搬迁 HEAD（M2.1，filter-repo 保史）。判定：决策席末节填。

## 职责
LLM 分析员工具集——契约/工具执行/热读沙箱。

## 边界
- **上游**：internal/data/symdig, internal/ir/notify
- **下游**：internal/store/pgstore

## 结构
14 个 go 文件（测试 5）。dNNN_ 前缀文件 0（M2.1 入场门已按关注点改名）。

## 测试
- 测试文件 5；依赖 TestDSN/storetest 的 1
- 超限文件：无

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
