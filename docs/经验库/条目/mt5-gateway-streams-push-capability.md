---
schema_version: 2
id: mt5-gateway-streams-push-capability
kind: server_fact
risk: medium
status: verified
keywords: [mt5, mtapi, 网关, Streams, server-streaming, OnQuote, OnOrderProfit, 推送, push, 事件驱动, 触发器, grpcurl, proto定义不等于实装的边界]
scope: [adapter, mt5, fact, ops, probe]
summary: MT5 网关 Streams 服务 9 个 server-streaming 全实装（OnQuote 12s 实拍帧/OnOrderProfit 20s 实拍账户字段）——「proto 定义≠实装」陷阱在服务方法层不适用，事件驱动采集可行（D-099 已落地并部署验证）
first_seen: 2026-08-24
last_verified: 2026-09-26
skills: [KB维护]
evidence_refs:
  - type: server_fact
    source: mt5grpc3.mtapi.io
    query: grpcurl list + MT5/Streams OnQuote（EURUSDm 订阅）+ MT5/Streams OnOrderProfit（SubscribeOrderProfit）
    observed_at: 2026-08-24
---

## 事实/症状

1. **Streams 服务 9 个 server-streaming 全部实装**（grpcurl list 实测）：Events（统一流）/ OnQuote / OnOrderUpdate / OnOrderProfit / OnMarketWatch / OnTickValue / OnTickHistory / OnMail / OnOpenedOrdersTickets。
2. **OnQuote 实拍**：Connect → Subscribe(EURUSDm) → OnQuote 流 12 秒收到 USTECm 实时报价帧（Bid/Ask/Time 字段有值）——推送真实可用，非空壳。
3. **OnOrderProfit 实拍**：SubscribeOrderProfit OK → 流 20 秒收到 Balance=1809.35 / Equity=1529.07 / **Margin=3.57e-05（≈0，印证 Exness 无限杠杆→0 值 fact 正确）** / FreeMargin / Profit=-280.276 / Orders（Ticket 2355655056）——账户级字段全实装。
4. **core 侧此前从未消费任何流**（mt5.go a.streams 已建未用，全链路 unary 轮询）——「网关无事件推送（纯 RPC）」是复审方误判（未查 proto 就下结论，违以事实出发），业主指正正确。
5. **D-099 落地实证**（2026-08-24 部署验证）：MarginFactSource 每 venue 1 条 OnOrderProfit 流当触发器，5/5 venue attach 后 3 秒内首采完成（旧路径 1h 周期，52993526 曾缺 61 分钟）；无 subscribe/recv/reconnect 失败日志=流全部订阅成功。

## 根因或策略解释

- **「proto 定义≠实装」陷阱的边界**：字段级（SymGroup margin 字段，见 [[mt5-symgroup-margin-fields-not-served]]）确实存在 proto 有定义但实拍不返回；但**服务方法级**（Streams 的 9 个 RPC）grpcurl list 实测全实装——两类陷阱不可混用。下「无推送」结论前必须 grpcurl list 查服务 + 实拍一帧，两者任一不做=失职。
- 服务方法实装 ≠ 字段都返回：OnOrderProfit 流内字段仅证「账户级字段实装」，动态占用保证金（Margin）≠ 杠杆率，不可直接换算——流内字段当触发器不消费是正确用法（D-099 设计）。

## 处置与后续

- 事件驱动改造先例（D-099）：**streaming 当触发器**（subscribe+recv 帧→unary 补采）+ 首采兜底 + 防抖 + IsSessionLost 重连退避，模式可复用（自动平仓立项将消费 Orders 字段）。
- 流重连要点：RST_STREAM/会话失效 → IsSessionLost 判定 + reconnectSession（复用 conn 重调 Connect，禁再 Dial）+ 重订阅，退避 1s/5s/30s 有界。
