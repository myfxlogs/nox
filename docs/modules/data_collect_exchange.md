# internal/data/collect/exchange

> 事实截至搬迁 HEAD（M2.1，filter-repo 保史）。判定：决策席末节填。

## 职责
CEX WS/REST 采集——binance/okx/gate 行情与私流，写 facts/ticks。

## 边界
- **上游**：—（本票消费者后续 M2.x 挂入）
- **下游**：internal/data/bus, internal/data/fact, internal/data/keydomain, internal/data/venue, internal/infra/dephealth, internal/platform/alerttext, internal/platform/httpx, internal/platform/safego, internal/platform/signutil, internal/store/pgstore

## 结构
87 个 go 文件（测试 40）。dNNN_ 前缀文件 0（M2.1 入场门已按关注点改名）。

## 测试
- 测试文件 40；依赖 TestDSN/storetest 的 1
- 超限文件：internal/data/collect/exchange/gate_subscribe_frame_test.go:611; internal/data/collect/exchange/exchange_history_test.go:598

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
