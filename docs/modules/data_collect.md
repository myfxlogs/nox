# internal/data/collect

> 事实截至搬迁 HEAD（M2.1，filter-repo 保史）。判定：决策席末节填。

## 职责
venue 采集器框架——fact_emitter+sched 调度基座。

## 边界
- **上游**：internal/data/adapter
- **下游**：internal/data/bus, internal/data/fact, internal/platform/safego

## 结构
138 个 go 文件（测试 58）。dNNN_ 前缀文件 0（M2.1 入场门已按关注点改名）。

## 测试
- 测试文件 58；依赖 TestDSN/storetest 的 5
- 超限文件：internal/data/collect/exchange/gate_subscribe_frame_test.go:611; internal/data/collect/exchange/exchange_history_test.go:598; internal/data/collect/cmc/ws_v21_test.go:560; internal/data/collect/cmc/ws_test.go:442; internal/data/collect/cmc/auth_test.go:323; internal/data/collect/cmc/collector_test.go:646; internal/data/collect/trx/apr_test.go:455; internal/data/collect/trx/collector_test.go:920

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
