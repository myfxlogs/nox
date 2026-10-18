---
name: bn-fapi-account
description: binance USDⓈ-M 合约账户域端点能力账（D-608）
---
# bn-fapi-account
## 证据
- `docs/能力账/bn-api-probe-2026-10-18-o0-b095803.pb`（fapi n=27 ok=13——st404×6/st400×5/st429×3）
- `docs/能力账/bn-api-probe-2026-10-18-o150.pb`（fapi n=36 ok=23——st200×23/st400×10/st404×3）
## 实测 OK 采样
- `GET /fapi/v2/account`/`/fapi/v2/positionRisk`（仓位族签名读 ok）`positionSide/dual`（dualSidePosition）`symbolConfig`（isAutoAddMargin/leverage/maxNotionalValue）`symbolAdlRisk`（ADL 排队位）`rateLimit/order`（rateLimitType/intervalNum）`income`（incomeType 枚举）`userTrades` 签名读 ok
## 坑注记
- **429 可见**（USED-WEIGHT 预算粘滞——probe 撞生产限频窗三帧 429 如实记）：签名读族规划须叠生产预算裕量。
- **400/404 族**：参数缺面（symbol 定向端点裸跑无参）+404 死端点（v1→v2 迁移残影——404=真死如实标）。
- **mutating**：`/fapi/v1/order*`/`positionSide` POST 全族黑名单零跑。
