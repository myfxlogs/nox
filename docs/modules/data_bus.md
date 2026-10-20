# internal/data/bus

> 事实截至搬迁 HEAD（M2.1，filter-repo 保史）。判定：决策席末节填。

## 职责
进程内事件总线——Quote/Fact/持仓变更主题发布订阅。

## 边界
- **上游**：internal/data/adapter, internal/data/cexscan, internal/data/collect, internal/data/collect/exchange, internal/data/marketstate, internal/ir/notify, internal/platform/usdnotional
- **下游**：internal/data/keydomain, internal/platform/safego

## 结构
20 个 go 文件（测试 8）。dNNN_ 前缀文件 0（M2.1 入场门已按关注点改名）。

## 测试
- 测试文件 8；依赖 TestDSN/storetest 的 0
- 超限文件：无

## 观察（只记事实）
- 搬迁纯移动+import 路径改写+文件名去票号；语义零改动。
- TickSampler 写径带 ticks 表 venue_type 白名单滤（`ticksVenueTypes`=ticks_venue_type_chk 代码侧镜像）——非通道型帧（如 cmc:cfd）不落库只记首见一条；写失败日志同键同文案 10min 冷却。qbus 现态面不截。

## 判定（决策席）
| 项 | 判定 | 理由 |
|---|---|---|
| 包整体 | | |
