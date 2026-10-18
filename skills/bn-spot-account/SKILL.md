---
name: bn-spot-account
description: binance spot 账户域端点能力账（D-608）——/api/v3/account 族实测面
---
# bn-spot-account
## 证据
- `docs/能力账/bn-api-probe-2026-10-18-o0-b095803.pb`（api 域 n=59 ok=19——st200×19/st400×23/st404×17；USED-WEIGHT 均值 271）
- `docs/能力账/bn-api-probe-2026-10-18-o150.pb`（api 段并入）
- 清单=`docs/能力账/binance_endpoints.gen.txt`（官方三源并集枚举——MUTATING 前缀黑名单跳件）
## 实测 OK 采样（10-18 窗）
- `GET /api/v3/account`（w=181）——balances+commission 字段齐=账户面活
- `/api/v3/allOrderList`（w=211，rows 空=零挂单如实）、`/api/v3/orderList` 族签名读 ok
- 交易对查询类 `/api/v3/exchangeInfo`（权重头随窗变——USED-WEIGHT 实拍非文档值）
## 坑注记
- **凭据域**：`exchange_configs` venue=binance simulated=f——签名面缺凭据即「凭据缺」如实标零伪造。
- **400 族**=参数缺/域格式（时间窗 symbol 过滤类参数缺失——probe 裸跑无参数占位=预期败非能力缺）。
- **mutating**：`POST /api/v3/order*` 全族黑名单机械分离——未实测如实缺。
