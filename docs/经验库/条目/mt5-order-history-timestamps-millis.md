---
schema_version: 2
id: mt5-order-history-timestamps-millis
kind: pitfall
risk: medium
status: verified
keywords: [mt5, mtapi, OrderHistory, 时间戳, 毫秒, UnixMilli, 取证, 单位]
scope: [adapter, mt5, probe]
summary: mtapi OrderHistory 的 Order.OpenTimestampUTC/CloseTimestampUTC 是 int64 毫秒（非秒）——当秒解析得 58605 年；OpenedOrders 却用 *timestamppb.Timestamp（安全），两接口单位不一致
first_seen: 2026-08-25
last_verified: 2026-09-26
skills: [KB维护]
evidence_refs:
  - type: doc
    ref: internal/adapter/gen/mtapi/mt5/mt5.pb.go:3862（OpenTimestampUTC int64 字段）
  - type: doc
    ref: internal/reconcile/reconcile_queriers.go:244（OpenedOrders 走 tsToTime(*timestamppb.Timestamp)，单位安全）
  - type: server_fact
    source: mt5grpc3.mtapi.io MT5/OrderHistory（Exness 277842155）
    query: OrderHistory #4 双腿开/平仓时间戳
    observed_at: 2026-08-25
---

## 事实/症状

直连 mtapi 网关取 `MT5/OrderHistory` 时，响应 `Order` 消息的时间戳是 **int64 毫秒**（`OpenTimestampUTC=1787242904690` → `time.UnixMilli()`），**不是秒**。用 `time.Unix(t, 0)` 或直接当秒解析会得到 **58605 年**这类离谱年份。

且同网关不同接口**单位不一致**：`OpenedOrders`（持仓）响应时间戳是 `*timestamppb.Timestamp`（`GetOpenTime()` → `tsToTime` 安全转换），而 `OrderHistory`（历史单）响应是 int64 毫秒——proto 字段名（`OpenTime` vs `OpenTimestampUTC`）差异是信号，不要跨接口假设同一单位。

## 根因或策略解释

- mtapi 网关对 `OrderHistory` 透传 MT5 服务器原始字段（`OpenTimestampUTC` 系 ms 的 datetime 内部表示），未规范化为 protobuf Timestamp。
- proto 字段形状（int64 vs Timestamp）不携带单位语义，单位只能靠**实测一条真单**定案（本次以 LOG 记录的 8-20 16:21:44 开仓时刻反向验证毫秒换算精确吻合）。

## 处置

- 读 `OrderHistory` 时间戳：一律 `time.UnixMilli(o.GetOpenTimestampUTC()).UTC()`，先与已知业务时刻对拍再采信。
- 写只读取证/回填工具时，先查接口返回类型——`*timestamppb.Timestamp` 走 `AsTime()`，int64 先验明单位再换算。
- 新代码若需历史单开/平仓时间：优先复用生产 `reconcile` 的 querier 模式（`tsToTime`），不要另起炉灶解析原始字段。

## 证据

- check: 临时取证工具 `scripts/mt_ordhist` 初版当秒解析 `GetOpenTimestampUTC` → 输出年份 58605
- result: 改 `time.UnixMilli` 后 open=1787242904690ms = **2026-08-20 16:21:44 UTC**，与 LOG 开仓时刻精确吻合（铁证）
- ref: 会话 2026-08-25（roll 实验 #4 终结判定，D-102）

## 反证条件

- check: mtapi 网关升级 OrderHistory 时间戳为 protobuf Timestamp 或秒
- falsifies_when: 再次直连实拍 `OpenTimestampUTC` 数值 < 10^12（秒量级）→ 单位假设失效，需重验

## 适用边界

- mtapi.io 网关 `MT5/OrderHistory`；其他 MT 网关（自建）需各自对拍。
- 仅指 `OrderHistory.Order` 的 int64 时间戳字段；`OpenedOrders` 的 `*timestamppb.Timestamp` 路径不受影响。
- 相关条目：[[mtapi-gateway-query-procedure]]（网关直连取证全流程）、[[grpc-server-stream-reconnect-on-break]]（D-101）。
