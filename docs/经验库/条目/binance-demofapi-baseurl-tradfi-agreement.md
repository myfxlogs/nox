---
schema_version: 2
id: binance-demofapi-baseurl-tradfi-agreement
kind: pitfall
risk: high
status: verified
keywords: [binance demo, demofapi, base_url, /fapi 前缀, nginx 403, TradFi-Perps, -4411, stock/contract, XAUUSDT]
scope: [binance, demo-fapi, cex-proxy, exchange_configs, 执行验证, XASSET_CARRY]
summary: demofapi base_url 必须含上游 /fapi 段（代理路由前缀≠API 前缀）否则 nginx 403；TradFi 类 perp（XAUUSDT 等）下单前账户须签 TradFi-Perps 协议（POST /fapi/v1/stock/contract）否则 -4411
first_seen: 2026-09-18
last_verified: 2026-09-18
skills: [事实核验, 生产取证]
evidence_refs:
  - type: doc
    ref: internal/exec/cex_executor_binance.go:36
  - type: doc
    ref: internal/dashboard/account_monitor.go:160
  - type: server_fact
    source: cex
    query: POST /demofapi/fapi/v1/order（签名+token）→ -1121 JSON；POST /demofapi/v1/order → nginx 403 HTML
    observed_at: 2026-09-18T05:24:00Z
---

## 事实/症状

Binance demo 执行行（exchange_configs id=25）回登后下单/持仓查询全 403，响应为 nginx 默认 HTML 页（非 Binance JSON 错误）。修复 base_url 后第二个断点：下单返回 `http 400 {"code":-4411,"msg":"Please sign TradFi-Perps agreement contract fapi."}`。

## 根因或策略解释

**断点 A（403）**：executor 端点是相对常量（perp=`/v1/order`、spot=`/api/v3/order`），最终 URL=base_url+端点。主网行 base=`…/fapi` 隐含了上游 API 前缀所以拼对；demo 行 base=`…/demofapi` 只是**代理路由前缀**，上游 demo-fapi.binance.com 仍需 `/fapi`——`/demofapi/v1/order` 在代理无路由 → nginx 403。正确 base=`…/demofapi/fapi`。教训：代理路由前缀与上游 API 前缀是两层，base_url 必须写到上游根（含 `/fapi`）。

**断点 B（-4411）**：XAUUSDT/PAXGUSDT/XAUTUSDT 属 Binance TradFi-Perps 产品线（2025-12 API 新增），下单前账户必须签署 TradFi-Perps 协议。签署端点=`POST /fapi/v1/stock/contract`（HMAC 签名、demo 同源有效）。协议状态是余额/限额之外的第三个账户前置条件——**demo 账户初始化清单：注资→杠杆→签署 TradFi 协议**。

## 处置

- exchange_configs binance demo perp 行 base_url 写 `https://<proxy>/demofapi/fapi`（主网行 `…/fapi` 不变）
- 诊断手法：403+HTML=代理/WAF 层拒（先怀疑路由/token）；Binance 业务错=JSON `{code,msg}`——响应体格式即故障域指纹
- 遇到 -4411：替该账户调 `POST {base}/v1/stock/contract`（签名）一次即可，返回 `SUCCESS` 后 TradFi perp 可下单
- 新增 CEX 行后验证锚：签名 GET 持仓端点 200 → 废单探针（invalid symbol 应得 JSON -1121 而非 403 HTML）→ 再放量

## 证据

- 2026-09-18 05:24 posID=486/487 两腿 LEGGED（MT 腿成、binance 腿 403）→ 修 base_url 后 -4411 → 签署后 posID=488 OPEN 双腿 FILLED（XAUUSD247m BUY 0.01lot + XAUUSDT SELL 1）
- 手动签名 `GET /demofapi/fapi/v2/positionRisk` 200、`POST` 废符号探针得 JSON -1121（路径/auth 全通的旁证）
- 同源相关条目：cex-demo-funding-simulated（demo funding 仿真）、demo-symbol-coverage-narrower-than-live
