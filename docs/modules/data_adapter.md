# internal/data/adapter

> 事实截至搬迁 HEAD（M2.1，filter-repo 保史）。判定：决策席末节填。

## 职责
MT5 网关——mt4/mt5 gRPC 通道、私有 WS 事件（持仓/订单/保证金）入 fact/bus。

## 边界
- **上游**：—（本票消费者后续 M2.x 挂入）
- **下游**：internal/data/adapter/gen/mtapi/mt5, internal/data/bus, internal/data/collect, internal/data/fact, internal/data/keydomain, internal/data/venue, internal/infra/dephealth, internal/platform/safego

## 结构
46 个 go 文件（测试 24）。dNNN_ 前缀文件 0（M2.1 入场门已按关注点改名）。

## 测试
- 测试文件 24；依赖 TestDSN/storetest 的 0
- 超限文件：internal/data/adapter/mt_margin_source_test.go:356; internal/data/adapter/mt_order_events_test.go:350; internal/data/adapter/mt_session_test.go:455; internal/data/adapter/mt_quote_stream_test.go:302; internal/data/adapter/gen/mtapi/mt5/mt5.pb.go:16304; internal/data/adapter/gen/mtapi/mt5/mt5_grpc.pb.go:4402; internal/data/adapter/gen/mtapi/mt5/mt5connect/mt5.connect.go:3525

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
