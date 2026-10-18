---
name: bn-sapi-wallet-earn
description: binance sapi 钱包+earn 域端点能力账（D-608）
---
# bn-sapi-wallet-earn
- 已踩坑：earn history 正确端点=`/sapi/v1/simple-earn/flexible/history/redemptionRecord`（错前缀 `/sapi/v1/earn/...` 404 实证）；`asset/transfer` type 枚举不含 earn 对（17 枚举清单在册=划转盲区根因）。
- 端点证据：`docs/能力账/binance_endpoints.md` sapi-wallet-earn 段。
