---
name: okx-private-ws
description: okx 私有 WS 面能力账——balance_and_position/account 频道、心跳刷源、快照行、键域归一、活性探针设计
---
# okx-private-ws
## 证据
- 实现件：`internal/data/collect/exchange/okx_private_ws_events.go`（事件→快照行）、`okx_private_ws_heartbeat.go`（活性心跳）
- SG 产验：`fin_venue_account_snapshot` `src=okx_event` `venue_type=unified` 行 ~5s 一拍（balance_and_position 含浮盈变动=自带心跳）；capitalview 私面败件清零
- JIT 事件源：`JitOkxEarnEvent` 由 account push 钩子触发（外部入金/赎回到账/成交结算全覆盖）
## 频道与口径
- `balance_and_position`：余额+持仓变动推送——**一切余额变动都是事件**（成交/费用/入金/赎回）；落 `okx_event` 快照行
- 快照行：`Venue=okx, VenueType=unified, Currency=USDT`；`EquityRaw=Σdetails eqUsd`，`FreeMarginRaw` 不采=equity 回退
- 心跳：`okx_private_ws_heartbeat` 3min 拍刷末帧写行——防「静户无推送→快照陈→误杀」
## 坑注记
- **funding 户不在频道面**：account/balance_and_position 只盖交易户——资金户余额唯 REST `/asset/balances`（钦定保留面）；JIT 闲额混源=unified〔快照须鲜<5min〕+funding〔REST〕，快照陈=fail-closed 不单边判。
- **键域归一**：armed 腿键=okx:perp/spot 但统一账户行只落 unified——探针/供行两侧都须归一，单侧归一=另侧致盲（返修②实证）。
- **活性探针量对象**：探「快照行新鲜度」而非「事件到达率」——静户事件零到达≠WS 死，须靠心跳行活性判定。
- **告警节流**：监护缺告警须状态跳变才发，事件级连发=洪刷（返修①实证）。
- **残面·querier 首探噪声**（2026-10-23 产验在案）：`account-monitor` 的 cex querier 每拍仍对 okx:spot/perp/futures 发 REST first-poll→被私面闸挡刷 `first-poll error`（5s 级）——闸工作正确但 querier 不识「WS 快照已鲜则免探」。修法=首探前查 okx_event 快照龄，鲜则跳探；陈/缺再 REST 兜底。小票级，未施工。
- **WS login 握手**（重建件）：`op=login` args=`{apiKey,passphrase,timestamp,sign}`，`sign=base64(HMAC-SHA256(secret, ts+"GET"+"/users/self/verify"))`；login/权限错误 fail-loud 重连不静默退化。端点：`wss://ws.okx.com:8443/ws/v5/private`（公开面 `/public`）；代理前缀约定 `wsViaProxyURLs("/okxws/ws/v5/private", 直连URL)`。conn 单写者契约（gorilla writeMu）。
