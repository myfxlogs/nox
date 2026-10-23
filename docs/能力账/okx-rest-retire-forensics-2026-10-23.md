# okx REST 退役→WS 固化取证（前置钉件 2026-10-23）

## 行情 REST 面现状
- funding REST 5min 轮询：**已退役**（D-518 S3 在案——WS funding-rate 主供）
- funding_history 24h REST：历史回填域（非实时行情面）——保留冷调用
- depth REST（FetchCexDepthsVenue okx）：books5 ws 主供后改 **闸关默认 fail-closed**
  ——`NOX_DEPTH_REST_OKX=1` 方可开拍；ws 断=数据缺不开单（不静默降级=风控哲学对版）
- instruments REST：冷调用保留（bootstrap 目录拉取，非实时面）

## WS funding-rate 帧载荷 vs REST 消费字段（逐字段核）
| 消费字段 | WS funding-rate 帧 | REST /public/funding-rate | 盖 |
|---|---|---|---|
| fundingRate | `fundingRate`(string) | `fundingRate` | ✅ |
| fundingTime（下次结算） | `fundingTime`(ms) | `fundingTime` | ✅ |
| nextFundingTime（再下期） | `nextFundingTime` | `nextFundingTime` | ✅ |
| interval 推导 | fundingTime−nextFundingTime 差 | 同 | ✅ |
**裁定**：字段面全盖——funding REST 行情轮询可砍（事实已砍）。

## 私面 WS 账户频道载荷取证（迁移前置钉）
okx 私面 REST 现用面（grep 实证）：`/api/v5/account/balance`（earn 归集/账户快照）、`/api/v5/account/positions`、exec `trade/order`。
okx 私有 WS 频道候选：`account`（余额+保证金快照推送）、`balance_and_position`（余额+持仓合并帧）、`orders`（订单流）。
**覆盖裁定**：`balance_and_position` 盖 balance+positions 双 REST 面〔事件驱动=earn 回腿纯事件化前置件〕；exec trade 频道迁为终态分票（exec REST 单发加固件过桥）。
