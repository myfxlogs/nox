---
schema_version: 2
id: baseline-rate-is-not-opportunity
kind: pitfall
risk: medium
status: verified
keywords: [基准费率, funding 套利, 假机会, 超额口径]
scope: [detector, evaluator]
summary: 同所 funding 对冲平静期的收益=行业基准费率（≈10.95%/年）人人可收无 edge；机会定义必须=扣基准后的超额
first_seen: 2026-08-23
last_verified: 2026-08-31
skills: []
evidence_refs:
  - type: server_fact
    ref: okx/gate 公开 API fundingRate=0.0001/8h ×3×365=10.95% 与 facts 表三 venue 同拍同值对拍一致
    verified: 2026-08-23
  - type: decision
    ref: docs/funding面重评判定书.md（D-084 前置）
  - type: server_fact
    ref: D-172 S5 实证——三所 facts max 全部恰=10.95 但负值到 -81%（非对称），Binance 公式 Premium+clamp(0.01%-Premium,±0.05%) 当 premium≈0 时费率恒=0.01%=默认值非 venue clamp（cap=±0.3%/8h=328.5% 年化）；D-171 报告「剔除 value≥10.949 cap 样本」推理有误（实为默认费率样本非截断极值）但对 funding 差分析影响可忽略（默认费率样本跨所差≈0）
    verified: 2026-08-31
---

# 基准费率不是机会：funding 扫描的超额口径

## 症状

同所 FUNDING_HEDGE（spot+perp 对冲）在平静期扫出满屏「年化 10.95% 净 6.45%」的机会卡，且**三个 venue 多符号同拍完全同值**——初判采集污染，实为真实市场：全部回归行业基准费率。

## 根因

- 基准费率（okx/binance/gate 0.01%/8h）年化 ≈10.95%，平静期所有品种回归该值。
- 检测门槛「净年化 ≥4%」以绝对值衡量，基准本身就过线 → 基准被当机会。
- 收基准 = 无 edge（人人可收），还要付出资金占用/负费率段倒贴/进出摩擦（0.45%/次，保本 15-23 天）。

## 通用解法

**机会 = 扣基准后的超额**：对冲类（spot+perp，**无论同所跨所**）毛收益先减基准费率再过门槛（配置化基准值，默认 10.95%）——spot+perp 结构无差分，收益恒=perp 腿单边 funding（D-084 实施修正①：初版误把「跨所=差分」套到 spot+perp，跨所版被业主实证抓出仍推基准）；只有真差分结构（perp×perp 跨所费率差、跨所价差）不减。判定口诀：先问「这个结构收几边费率/价差？」——收一边=减基准，收两边之差=差分不减。对任何 carry/funding 扫描成立：先问「这个收益是市场给所有人的，还是结构给我的？」——前者是基准（噪音），后者才是机会。

## 证据

- 2026-08-23 重现: TRXUSDT 三 venue 同拍 10.95 与 API 对拍一致（基准非污染）| ref: 生产库 facts + okx/gate API
- 2026-08-23 重现: 费率 6 日 −15.6%↔+37.3% 摆动，两日负均值 | ref: facts okx TRXUSDT 逐日聚合
