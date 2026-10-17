---
schema_version: 2
id: probe-shares-prod-egress-ip-ban
kind: pitfall
risk: critical
status: verified
keywords: [418, -1003, IP 封禁, rate limit, probe, 探测, pace, 权重预算, binance, funding stale, 共享出口 IP]
scope: [ops, venue-probe, capability-ledger, binance, SG]
summary: venue 探测类跑数与生产共享出口 IP——pace 节流只控自身节奏不控生产权重预算份额，一封 418 即 abort 禁续扫
first_seen: 2026-10-17
last_verified: 2026-10-17
skills: [生产取证]
evidence_refs:
  - type: server_fact
    ref: 'fapi.binance.com premiumIndex → http 418 {"code":-1003,"msg":"Way too many requests; IP(192.229.85.35) banned until 1792238250774"}'
    verified: 2026-10-17
  - type: server_fact
    ref: facts funding binance BTCUSDT 断流窗 10:14:58→11:15:36Z（src=binance_rest_markprice）；xasset-monitor 告警 stale 1h0m3s>15m 正确触发
    verified: 2026-10-17
  - type: doc
    ref: docs/handoff/STATE.md 事故注记 2026-10-17 11:2xZ
---

## 事实/症状

bn-capability 票 probe 在 SG 生产机全谱扫 568 binance 端点（pace 100ms）→ 币安 -1003 出口 IP 封禁 ~1h（fapi/dapi REST 全 418，spot api 未封，WS 放行）→ 生产面 capitalview 校准/markPrice REST/step-meta 全瘫 → funding 行断流 → xasset-monitor stale 告警正确触发。封禁自然解除后自愈。

## 根因或策略解释

pace 节流语义错配：100ms pace 是「探针自身节奏」，不是「生产出口 IP 权重预算的份额」。生产系统自身 REST 调用（capitalview 校准/step-meta/markPrice fallback）已在消耗同一 IP 的权重额度；探测叠加=预算打穿→-1003。且 probe 触发 418 后未停手续扫，把封禁滚雪球。

## 处置

- venue 探测/probe 类跑数票面必带「生产权重预算核算行」——先核算系统自身 REST 权重占用再定探测 pace；
- 418/-1003 一封即 abort 禁续扫（续扫只加深封禁时长——10-17 实证：连撞两轮封禁记忆叠加，解禁窗比首轮报时点更长）；
- **解禁判定=ping 200 实测为准，不凭 venue `banned until` 报时点提前跑**（报时点是 venue 侧预期值，撞尾巴续跑=再封）；
- **封禁形态实拍=分钟级短封**（撞线即封几分钟/停手即解）——**正解=批化**（≤150 端点/批×批间 ≥5min 冷却+批前 ping 预检），pace 升档救不了「单跑累计权重打满滚动窗」；
- 探测节奏核算口径=系统自身占用+探测叠加≤venue 限频预算；
- 告警侧不变：stale 告警正确履职即验证闭环。

## 证据

- check: `curl https://fapi.binance.com/fapi/v1/premiumIndex?symbol=BTCUSDT`（SG）
- result: http 418 + `IP(192.229.85.35) banned until 1792238250774`；spot api ping=200 未封；funding facts 断流窗 61.5min 实拍
- ref: STATE 事故注记 2026-10-17 11:2xZ（同 commit 链 bnprobe 5fix @2fd963fb3 时间窗吻合）

## 反证条件

- check: 若封禁时序与 probe 跑数窗不重合（如系统自身 REST 权重先超限），则归因改判为「生产自身限速不足」
- falsifies_when: probe 未运行窗口内出现同型 -1003 封禁=主因不在 probe（本条目降级为「叠加加速因子」）

## 适用边界

适用于一切 venue 探测/能力账 probe/批量端点枚举类跑数；生产机与探测共享出口 IP 的场景；binance -1003 封禁族（其他 venue 同构适用）。
