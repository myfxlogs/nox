---
schema_version: 2
id: okx-perp-ctval-qty-conversion
kind: pitfall
risk: high
status: verified
keywords: [OKX, perp, swap, ctVal, 张数, 基础币, qty, 换算, 51008, Insufficient margin, LEGGED]
scope: [exec, cex_executor, okx perp, CEX qty 单位契约]
summary: OKX perp `sz` 单位=张数（每张 ctVal 基础币），系统 Order.Qty=基础币——直接传值 qty=2964 被当 2964 张（TRX ctVal=1000 → 名义~$100万）→ 51008 保证金拒单 → LEGGED。修法=placeOKX 查 instruments ctVal+lotSz，sz=floor(qty/ctVal/lotSz)×lotSz，Fill.Qty 转回基础币。
first_seen: 2026-08-27
last_verified: 2026-08-28
skills: [事实核验]
evidence_refs:
  - type: decision
    ref: D-125
  - type: doc
    ref: internal/exec/cex_executor_okx.go
  - type: server_fact
    source: cex
    query: POST /api/v5/trade/order qty=2962 → sz=2.96 张 sCode=0
    observed_at: 2026-08-28T00:00:00Z
    server_revision: ""
    response_hash: ""
---

## 事实/症状

OKX perp 下单返回 `51008 Insufficient USDT margin`，但账户余额充足。PERP_FUNDING binance 腿 FILLED + okx 腿 FAILED → LEGGED 单腿暴露（#374/#375/#376）。

## 根因或策略解释

OKX swap `sz` 单位=**张数**（contracts），每张=`ctVal` 基础币（TRX-USDT-SWAP ctVal=1000）。系统 Order.Qty=基础币数量（与 gate D-120 同源假设），qty=2964 TRX 被当 2964 张 → 实际 2,964,000 TRX 名义~$100万 → 保证金~$2万 >> 模拟盘 $4998 → 51008。

## 处置

`placeOKX` perp 分支：①查 `okxContractVal`（GET /api/v5/public/instruments 公开端点，per-executor 缓存）②`sz=floor(qty/ctVal/lotSz)×lotSz`（防 51121 lot size）③`Fill.Qty=accFillSz×ctVal` 转回基础币。

## 证据

- check: 真实 okx demo：qty=2962 → sz=2.96 张 → sCode=0；旧逻辑 2964 张 → 51008
- ref: D-125（decisions.md）

## 反证条件

- check: OKX 合约规格接口 ctVal 语义变更
- falsifies_when: 官方改 sz 直收基础币（不兼容变更）

## 适用边界

CEX perp qty 单位三所不统一：OKX=张数（ctVal 换算）、Binance=基础币、gate=合约数（quanto_multiplier）。新增 CEX perp executor 必须逐一验证 qty 单位契约，禁止假设「qty=基础币」跨所通用。
