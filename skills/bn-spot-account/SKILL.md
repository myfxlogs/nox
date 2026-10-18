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
- **能力探测频控注记**（注记#74②）：probe 批跑须预留生产权重余量——预检 ping=存活验证≠频限合规（418 封禁窗内 ping 仍 200 实证）；批跑限速窗（pace ≥500ms）+错峰生产高负载窗+418 即停 abort 禁续扫——烧穿共享出口 IP 权重=REST 兜底全灭形态（10-19 实证 37 次/4min）。
- **端点级记实（o0 残段补跑批 b135758/b135803）**：13 残端点全采=5 ok/5×400 缺参族/3×404——400 缺参族=裸跑无参数占位预期败（时间窗/symbol 定向类端点必带参）；权重闸接线坑=「闸件并入未接线首跑实证截批 used=1248>cap」详见 `docs/经验库/条目/bnprobe-weightcap-merged-unwired.md`。
