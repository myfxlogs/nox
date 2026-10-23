---
name: mt5-orders-streams
description: mtapi MT5 订单/流面——OpenedOrders/OrderHistory 族/Subscribe 族/Streams On* 实测能力+Deal 载荷教训锚（入金走 DealType_Balance=2）
---

# mt5 订单流面（实测 2026-10-17，证据=docs/能力账/mt5-rpc-probe-2026-10-17.json）

| RPC | 功能 | ICR | EXR | XMR | 关键坑 |
|---|---|---|---|---|---|
| OpenedOrders(Tickets) | 在仓单实读 | ok | ok | ok | Order.Ticket/Profit/Swap——bpMon 持仓卡源 |
| OpenedOrder | 单仓按 ticket 查 | err | err | err | 「OK null response」三户一致——空仓户/未知票=空体非故障，先 OpenedOrdersTickets 取票再查 |
| MarketWatchMany | 行情观察列表批量 | Unimplemented | Unimplemented | Unimplemented | 网关未实现（MarketWatch 面走 SubscribeMarketWatch 同 Unimplemented——观察单功能缺面在案） |
| SubscribeOrderProfit/SubscribeTickValue/SubscribeOrderUpdate | 分型订阅 | ok | ok | ok | 对应 On* 流帧开关——先订后流 |
| SubscribeMarketWatch/SubscribeOpenedOrdersTickets | 观察单/仓票订阅 | Unimplemented | Unimplemented | Unimplemented | 网关未实现（仓票面走 OpenedOrdersTickets 拉取替代） |
| OrderHistory | 已平仓史（充提 Balance 行+平仓盈亏） | ok | ok | ok | **OrderType_Balance(=100)=充提行**——mtIORound 只收此型；CloseTimestampUTC 毫秒；XMR 实证入金可滞后 balance 帧 ~48h 才现此面（退回㉖） |
| OrderHistoryPagination | 分页史 | Unimplemented | Unimplemented | Unimplemented | 网关未实现（三户一致；XMR 取证同理） |
| PendingOrderHistory | 挂单史 | Unimplemented | Unimplemented | Unimplemented | 同上 |
| Subscribe(Many)/UnSubscribe(Many) | 订阅动作（symbol 串） | ok | ok | ok | interval 可选毫秒节流 |
| Streams.Events | **聚合事件流**（一切 On* 的合流根） | ok（45s 窗） | ok | ok | 流帧=OnOrderUpdate/OnQuote/…分型；空仓户零帧常态 |
| OnQuote/OnOrderUpdate/OnOrderProfit/OnTickValue/OnMail/OnMarketWatch/OnOpenedOrdersTickets | 独立 server-stream | 短窗采样在案（零帧/err 如实） | 同 | 同 | **Deal 载荷教训锚（D-607）**：OnOrderUpdateReply.Deal 携 DealType 枚举——DealType_Balance=2=入金载荷（一直躺在 proto 里没人看）；充提事件监控正道=订阅此流非轮询 OrderHistory |

**坑**：流 RPC 无 token 时静默零帧——先 Subscribe 成功再 Events；断线自愈=wsrunner 重连+重订阅（adapter 范式）。
**magic number=ExpertId**：写侧 `OrderSendRequest.expertID`（field 10 optional）；读侧 `Order.ExpertId`（21）/`DealInternal.ExpertId`（23）/`OrderInternal.ExpertId`（26）——查单=OpenedOrders/OrderHistory 按 Ticket 过滤取该字段。
**符号方言硬坑**：每 broker 后缀不同（EXR/XMR 户 XAUUSD not exist 实拍、Exness=m 尾 BTCUSDm）——symbol 名一律 venue `SymbolList` 实读禁猜；GETQuote 族传裸名=「Symbol not exist」假故障。
**复跑**：同 mt5-account（--window 控窗）。

<!-- 方法全名索引：OpenedOrders OpenedOrder OpenedOrdersTickets OrderHistory PendingOrderHistory OrderHistoryPagination Subscribe SubscribeMany UnSubscribe UnSubscribeMany SubscribeOrderProfit SubscribeTickValue SubscribeOrderUpdate SubscribeMarketWatch SubscribeOpenedOrdersTickets Events OnOrderUpdate OnQuote OnTickValue OnOrderProfit OnMarketWatch OnMail OnOpenedOrdersTickets MarketWatchMany -->
## 平仓事件仲裁坑（ant 实证移植——nox 事件轨同病预防件）
- **部分平也发 close 事件**：close-kind push 命中≠全平——事件路径必进 RAW 仲裁：票在+量<源=部分平→仅刷快照；票消失=全平→迁移+删快照（ant PARTIAL-CLOSE-EVENT-ARBITRATION 实证）。
- **close push 不携余量**：push 报 remaining=0 不可靠——命中后仍须 OpenedOrders RAW 取余量；RAW 取证败=fail-closed UNKNOWN 禁猜 0（ant CLOSE-PUSH-REMAINING-ZERO 实证）。
- **票号 int32 溢出**：MT5 票域超 int32（Exness 实拍 2.66e9）——下游凡 int32 票槽皆截断回绕为负；VM/表侧票号须 uint64/合成句柄桥（ant MT5-TICKET-INT32-OVERFLOW 实证）。
- **双流归一 fail 语义**：fail 须 `close(done chan)`，send 端 `select{ch<-, <-done}`，Recv 端 `select{ch,done}`——只存 err 不关信号=Recv 永久阻塞；close(ch)=他 pump send panic（ant STREAM-MERGE-FAIL-SHAPE 实证）。
