---
schema_version: 2
id: okx-max-size-vs-max-avail-size
kind: pitfall
risk: high
status: verified
keywords: [OKX, max-size, max-avail-size, availBuy, availSell, maxBuy, maxSell, 保证金预检, 张数, 端点错位, avail="" invalid]
scope: [internal/exec, okx, perp, 保证金预检, D-429]
summary: OKX 两个相邻端点语义完全不同——/account/max-size 返 maxBuy/maxSell=合约张数（per instrument），/account/max-avail-size 返 availBuy/availSell=账户级 USDT 权益（与 instId 无关）；端点写错+字段名照旧解码→恒空串 fail-closed，而「顺手改字段名」修法会把张数 vs USDT 错位比较上线（BTC 方向 ~200× fail-open → LEGGED 风险）
first_seen: 2026-09-23
last_verified: 2026-09-24
skills: [KB维护, 事实核验]
evidence_refs:
  - type: commit
    ref: 69986df5
  - type: doc
    ref: docs/D-429设计与施工提示词-okx保证金预检max-size端点错位-2026-09-24-Devin.md
  - type: server_fact
    source: cex
    query: GET /api/v5/account/max-size?instId=XAU-USDT-SWAP&tdMode=cross → maxBuy=1049,maxSell=1054；同参 max-avail-size → availBuy=availSell=1514.1200611123584（=USDT availEq，BTC 同值）
    observed_at: 2026-09-23T17:00:00Z
    server_revision: ""
    response_hash: ""
---

## 事实/症状

`cex_executor_margin.go` 调 `/account/max-avail-size` 却按 `{"maxBuy","maxSell"}` 解码 → 两字段恒空串 → `avail="" invalid` fail-closed，501 复活链被拦。初诊误判为「字段名错位」，拟改 `availBuy/availSell`。

## 根因或策略解释

真根因=**端点错位**非字段名：官方文档两节相邻——`max-size` 返合约张数（maxBuy/maxSell per instId），`max-avail-size` 返账户级可用权益（availBuy/availSell，两 symbol 同返同一 USDT availEq 值实锤）。作者本意是 max-size（注释「张数」+szContracts 比较语义证明），URL 写错。

**自审推翻点**：若按初诊改字段名 availBuy/availSell，比较语义变成「张数 vs USDT 权益」——XAU 上限虚高 ~15%（1211 vs 真实 1054）、BTC ~200×（1211 vs 真实 5.38）→ 预检假放行→OKX 拒单→MT 腿已开=LEGGED 真钱残余。「看着对的修法」比原 bug 更危险。

## 处置

只换 URL `max-avail-size`→`max-size`，字段名/比较语义全不动；测试 fake 路由严格匹配路径+值换生产量纲；双锚防回漂：T4-A=fake 返 availBuy/availSell 形态→必须 `avail="" invalid` 响亮失败；T4-B=BTC 实拍量纲 maxSell"5.38"+sz=10→拒（v1 修法下会假放行）。

## 证据

- check: 签名直探双端点裸 JSON（上 evidence_refs）+ 部署后 `okx margin insufficient: sz 1017张 > avail 1053张×0.80` 正确拒开→业主调杠杆 5x→posID=502 双腿 FILLED
- result: 端点修复后闸语义正确（真拦超容单）且放行后执行通畅
- ref: D-429 @69986df5 / merge e40dfeed

## 反证条件

- check: OKX 文档若改版使 max-avail-size 返回 per-instrument 张数，或 max-size schema 变更
- falsifies_when: max-size 响应字段名变更或非张数单位（探针复验即知）

## 适用边界

OKX SWAP（cross/isolated 同构）；spot/margin 的 max-size 语义文档口径不同（现码仅 perp 用）。教训泛化：端点相邻≠语义相邻，「字段名对不上」先验 URL 再验字段——诊断顺序错一步=修出更深的 bug。
