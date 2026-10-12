# internal/store/pgstore

> 事实截至搬迁 HEAD（M2.1，filter-repo 保史）。判定：决策席末节填。

## 职责
PostgreSQL 读写层——全表族 DAO（含 seed/迁移重放）。

## 边界
- **上游**：internal/data/cexscan, internal/data/collect/cmc, internal/data/collect/exchange, internal/data/collect/trx, internal/data/macro, internal/data/marketstate, internal/data/symdig, internal/ir/notify, internal/platform/analyst, internal/platform/testutil/storetest, internal/research/scan
- **下游**：internal/data/fact, internal/data/keydomain, internal/data/normalize, internal/platform/alerttext, internal/platform/cryptoutil, internal/platform/errclass, internal/platform/notional, internal/review/verification

## 结构
235 个 go 文件（测试 115）。dNNN_ 前缀文件 0（M2.1 入场门已按关注点改名）。

## 测试
- 测试文件 115；依赖 TestDSN/storetest 的 25
- 超限文件：internal/store/pgstore/settlement_d193_test.go:331; internal/store/pgstore/hedge_flow_test.go:317; internal/store/pgstore/positions_test.go:328

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
