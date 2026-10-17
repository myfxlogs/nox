---
name: mt5-orders-streams
description: mtapi MT5 订单/流面——OpenedOrders/OrderHistory 族/Subscribe 族/Streams On* 实测能力+Deal 载荷教训锚（入金走 DealType_Balance=2）
---

# mt5 订单流面（实测 2026-10-17，证据=docs/能力账/mt5-rpc-probe-2026-10-17.json）

| RPC | 功能 | ICR | EXR | XMR | 关键坑 |
|---|---|---|---|---|---|
| OpenedOrders(Tickets) | 在仓单实读 | ok | ok | ok | Order.Ticket/Profit/Swap——bpMon 持仓卡源 |
| OrderHistory | 已平仓史（充提 Balance 行+平仓盈亏） | ok | ok | ok | **OrderType_Balance(=100)=充提行**——mtIORound 只收此型；CloseTimestampUTC 毫秒；XMR 实证入金可滞后 balance 帧 ~48h 才现此面（退回㉖） |
| OrderHistoryPagination | 分页史 | Unimplemented | Unimplemented | Unimplemented | 网关未实现（三户一致；XMR 取证同理） |
| PendingOrderHistory | 挂单史 | Unimplemented | Unimplemented | Unimplemented | 同上 |
| Subscribe(Many)/UnSubscribe(Many) | 订阅动作（symbol 串） | ok | ok | ok | interval 可选毫秒节流 |
| Streams.Events | **聚合事件流**（一切 On* 的合流根） | ok（45s 窗） | ok | ok | 流帧=OnOrderUpdate/OnQuote/…分型；空仓户零帧常态 |
| OnQuote/OnOrderUpdate/OnOrderProfit/OnTickValue/OnMail/OnMarketWatch/OnOpenedOrdersTickets | 独立 server-stream | 短窗采样在案（零帧/err 如实） | 同 | 同 | **Deal 载荷教训锚（D-607）**：OnOrderUpdateReply.Deal 携 DealType 枚举——DealType_Balance=2=入金载荷（一直躺在 proto 里没人看）；充提事件监控正道=订阅此流非轮询 OrderHistory |

**坑**：流 RPC 无 token 时静默零帧——先 Subscribe 成功再 Events；断线自愈=wsrunner 重连+重订阅（adapter 范式）。
**复跑**：同 mt5-account（--window 控窗）。
